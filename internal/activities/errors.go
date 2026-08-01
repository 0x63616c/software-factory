package activities

import (
	"context"
	"errors"
	"fmt"

	"github.com/0x63616c/software-factory/internal/clients/codexauth"
	"github.com/0x63616c/software-factory/internal/clients/github"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/temporal"
)

// The error types workflow code sees. They are the one translation of this
// service's error vocabulary into Temporal's, and the only strings a workflow
// or a retry policy may match on.
//
// They exist because a workflow cannot read a Go sentinel: an activity failure
// crosses a process boundary and arrives as an ApplicationError carrying a
// type string. Everything a workflow decides differently — pause, wait out a
// cooldown, fail the ticket — is decided from one of these.
const (
	// ErrTypeAuth is a credential that is wrong, revoked or under-permissioned.
	//
	// It PAUSES the dispatcher, which is the whole reason it is distinguished
	// from any other permanent failure. Without that, an installation that
	// cannot remove the `auto` label leaves the label on the issue, the
	// dispatcher lists the ticket again on the next poll, and the system spins
	// on a ticket it can never finish — a hot loop that nothing inside the
	// GitHub seam can bound, because the seam cannot remove a label it has no
	// permission to remove. Recorded on #333 and #339.
	ErrTypeAuth = "Auth"

	// ErrTypeRateLimit is a provider limit that a retry cannot get under. It
	// trips the dispatcher's cooldown breaker rather than pausing it: the
	// system is not broken, it is early.
	ErrTypeRateLimit = "RateLimit"

	// ErrTypeRuleset is a repository policy or merge grant that an operator can
	// repair while the target Merge Step remains within its retry deadline.
	ErrTypeRuleset = "GitHubRuleset"

	// ErrTypeNotFound is a resource that is gone — or, in a private repository,
	// one this installation cannot see, which GitHub reports identically.
	ErrTypeNotFound = "NotFound"

	// ErrTypeInvalid is a request the far side refused as malformed. Sending it
	// again sends the same thing.
	ErrTypeInvalid = "Invalid"

	// ErrTypePermanent is a permanent failure with no more specific kind.
	//
	// The string is "PermanentFailure" rather than "Permanent" because D1 pins
	// that literal and workflow RetryPolicies are written against it. A rename
	// here turns a permanent auth failure into an infinite retry loop, with a
	// green build — so the constant and the literal are kept identical
	// deliberately, and the test that asserts the string is the wall.
	ErrTypePermanent = "PermanentFailure"

	// ErrTypeTransient is everything else: a 5xx, a dropped connection, a
	// deadline. It is retryable, and it is typed anyway so that every activity
	// failure arriving at a workflow has a type it can switch on rather than a
	// Go type name that changes when someone rewraps an error.
	ErrTypeTransient = "Transient"

	// ErrTypeCINotConcluded is an expected retryable wait. AwaitCI sets its
	// exact next retry delay so pending CI never enters a workflow poll loop.
	ErrTypeCINotConcluded = "CINotConcluded"

	// ErrTypePredecessorMergeFenced preserves the target-run recovery outcome:
	// an older canceled run merged before its successor could proceed.
	ErrTypePredecessorMergeFenced = "predecessor_merge_fenced"
	// ErrTypeRunWorkerSessionLost identifies a lost generation-affine tool session.
	ErrTypeRunWorkerSessionLost = "run_worker_session_lost"
	// ErrTypeCIUnobserved identifies an exhausted exact-head CI observation window.
	ErrTypeCIUnobserved = "ci_unobserved"
	// ErrTypeHardDeadline is the target run's absolute execution ceiling.
	ErrTypeHardDeadline = "hard_deadline"
	// ErrTypeSemanticDeadline reserves time for terminal recording and cleanup.
	ErrTypeSemanticDeadline = "semantic_deadline"
	// ErrTypeUnresumableIncompleteAttempt is retained for target recovery rows
	// that predate durable AgentWorkflow conversation references.
	ErrTypeUnresumableIncompleteAttempt = "unresumable_incomplete_attempt"
)

// fail translates this service's error vocabulary into Temporal's, once.
//
// Two independent facts travel out of a client: *why* it failed, as one of this
// package's sentinels, and *whether a retry could help*, as work.ErrPermanent.
// They are separate bits and are translated as separate things — the type says
// why, and NonRetryable says whether. Collapsing them would make a retryable
// secondary rate limit non-retryable, which would fail a whole WorkTicket run,
// discarding every token already spent, to save the minute GitHub asked us to
// wait.
//
// op names what we were doing, because an error read in Loki at 3am has no
// stack beside it.
func fail(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}

	// Cancellation is not a failure. Temporal must see it as cancellation — an
	// activity that reported it as an application error would be retried into a
	// context that is already dead.
	//
	// This is checked BEFORE permanence, which is what makes a cancellation
	// wrapped inside a permanent error still a cancellation. A draining worker
	// cancels its activities on SIGTERM, and a clean drain reported as a
	// permanent application failure is a ticket that fails on every deploy and
	// is never picked up again.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", op, ctxErr)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", op, err)
	}

	return temporal.NewApplicationErrorWithOptions(
		fmt.Sprintf("%s: %v", op, err),
		errorTypeOf(err),
		temporal.ApplicationErrorOptions{
			NonRetryable: errors.Is(err, work.ErrPermanent),
			Cause:        err,
		},
	)
}

// errorTypeOf names why a failure happened.
//
// It matches on the github package's sentinels, which that package's own doc
// comment names as the only values its callers should match on. The order is
// most-specific-first and auth leads, because an auth failure is the one that
// stops the whole system rather than one ticket.
func errorTypeOf(err error) string {
	switch {
	// codexauth.ErrUnseeded means the codex-auth Secret does not exist or does
	// not parse (#344/#398): every ticket fails identically until a human
	// seeds it, which is exactly the "stop and page a human" case ErrTypeAuth
	// exists for, not an ordinary permanent failure this one ticket alone hit.
	case errors.Is(err, codexauth.ErrUnseeded):
		return ErrTypeAuth
	case errors.Is(err, github.ErrAuth):
		return ErrTypeAuth
	case errors.Is(err, github.ErrRateLimit):
		return ErrTypeRateLimit
	case errors.Is(err, github.ErrRuleset):
		return ErrTypeRuleset
	case errors.Is(err, github.ErrNotFound):
		return ErrTypeNotFound
	case errors.Is(err, github.ErrInvalid):
		return ErrTypeInvalid
	case errors.Is(err, work.ErrPermanent):
		return ErrTypePermanent
	default:
		return ErrTypeTransient
	}
}

// FailureKindOf reads back what a workflow needs to decide from an activity
// failure: whether the system is broken, merely early, or neither.
//
// It lives here rather than in workflows because it is the other half of fail —
// the type strings have one home, and a reader who changes one sees the other.
func FailureKindOf(err error) work.FailureKind {
	if err == nil {
		return work.FailureNone
	}

	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		switch app.Type() {
		case ErrTypeAuth:
			return work.FailureAuth
		case ErrTypeRateLimit:
			return work.FailureRateLimit
		}
	}
	return work.FailureOther
}
