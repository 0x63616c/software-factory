package work

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidRun reports a run-shaped value the workflows cannot run on: a
// policy with no timeouts, or a Run Worker template with no image.
//
// It is separate from the errors Config.Validate returns because the two are
// answered by different people. A bad Config arrives on a signal and is
// reported back through Status.ConfigError; a bad TargetRunPolicy or Run Worker
// template is a deploy that is wrong, and fails the workflow. Compare with
// errors.Is.
var ErrInvalidRun = errors.New("invalid run configuration")

// DispatcherTuning is what paces the loop and is not the operator's business.
//
// The poll interval and the orphan grace ARE the operator's business and live
// on Config, where GetStatus and UpdateConfig can reach them — a knob an
// operator cannot reach from the place they look is a knob that does not exist.
// What is left here are properties of the loop rather than settings: the
// history ceiling, and how long a ticket that just finished is protected from
// a re-claim.
type DispatcherTuning struct {
	// MaxHistoryEvents is when the dispatcher ContinueAsNews. A timer loop's
	// history is unbounded by construction, so this is not a tuning knob but
	// the thing that keeps the workflow alive — which is why it is here and not
	// on Config, where an operator could set it to something that stops the
	// loop bounding its own history.
	MaxHistoryEvents int

	// ReclaimCooldown is how long a ticket stays protected from being started
	// again after its run has just finished, even if a list of tickets
	// labelled `auto` still names it.
	//
	// It exists because that list comes from GitHub's issue index, which is
	// eventually consistent: #405 observed, on this project's own
	// infrastructure, a label removed a moment earlier still being returned by
	// a list query for some seconds before settling. Temporal workflow-ID
	// uniqueness already prevents two *concurrent* runs of one ticket; this is
	// what prevents a *sequential* re-claim once the first run has ended and
	// released its ID — a defensive lower bound on a race window this service
	// does not control, not an operator-tunable pace, which is why it sits
	// here rather than on Config.
	ReclaimCooldown time.Duration
}

// DefaultDispatcherTuning is the single source of these numbers.
//
// ReclaimCooldown of five minutes is several times the settle time #405
// observed (a few seconds, up to ~10s), leaving headroom for a slower moment
// without depending on a bound nobody has ever seen exceeded. It is also far
// shorter than "a human reads the outcome comment and re-adds the `auto`
// label to request another pass" ever takes in practice, so that path — the
// one every outcome comment tells a user to use — keeps working; it merely
// cannot be exercised in the same few minutes the ticket just finished in.
func DefaultDispatcherTuning() DispatcherTuning {
	return DispatcherTuning{MaxHistoryEvents: 2000, ReclaimCooldown: 5 * time.Minute}
}

// Validate reports why the dispatcher cannot loop on this tuning.
func (t DispatcherTuning) Validate() error {
	if t.MaxHistoryEvents <= 0 {
		return fmt.Errorf("%w: history ceiling must be positive, or the dispatcher never continues as new", ErrInvalidRun)
	}
	if t.ReclaimCooldown <= 0 {
		return fmt.Errorf("%w: reclaim cooldown must be positive, or a finished ticket is never protected from the "+
			"eventually-consistent list that named it (#405)", ErrInvalidRun)
	}
	return nil
}

// BranchName is the branch one run pushes its work to, and the branch its pull
// request is later found by.
//
// It has one home for the same reason WorkflowID does: the Run Worker creates it,
// the implement stage pushes it, and the worker asks GitHub what pull request
// is open on it. Three readers of one fact, so a second spelling is a run whose
// pull request can never be found.
//
// Neither part can carry attacker-chosen text — a ticket number is an integer
// and a RunID is a UUID Temporal mints — so no issue author can steer what gets
// pushed or which branch is queried.
func BranchName(ticketNumber int, runID string) string {
	return fmt.Sprintf("software-factory/ticket-%d/%s", ticketNumber, runID)
}

// RunWorkerBranchEnv is the environment variable the Run Worker reads its
// branch from. It is part of the Run Worker image contract, like WorkspaceRoot.
const RunWorkerBranchEnv = "SF_BRANCH"

// PullRequest is a pull request GitHub reported, as GitHub reported it.
//
// The URL comes from GitHub's own response and never from model output. That is
// not a style preference: a URL lifted from a stage's document is
// attacker-influenced text, and `https://github.com@evil.example/x` renders as
// an autolink to evil.example (#371). Asking GitHub what is open on a branch we
// named ourselves is unforgeable.
type PullRequest struct {
	Number int
	URL    string

	// State is the lifecycle state GitHub reported. Closed alone never proves a
	// merge: target merge reconciliation requires an explicit confirmation too.
	State PullRequestState

	// HeadSHA and BaseSHA are GitHub's current branch tips, never model prose.
	HeadSHA string
	BaseSHA string

	// Mergeability is GitHub's current assessment. Unknown means it is still
	// computing and must not be guessed into a conflict verdict.
	Mergeability PullRequestMergeability

	// MergeSHA is meaningful only alongside an authoritative merged confirmation.
	MergeSHA string

	// Draft is GitHub's reported draft state. It distinguishes draft-first
	// pull requests from ready pull requests owned by workflows begun before
	// the draft-first rollout, so terminal cleanup never clears `auto` after
	// failing to protect a legacy ready pull request.
	Draft bool

	// NodeID is GitHub's GraphQL global identifier for this pull request,
	// distinct from Number, which is the REST API's identifier. The
	// draft-state mutations accept only this one — REST has no path to change
	// an open pull request's draft state. See clients/github's graphql.go.
	NodeID string

	// Title and Body are this pull request's current title and description,
	// as GitHub itself reports them. They exist only so create-or-edit can
	// tell whether a later push actually changed either before spending an
	// Edit call — never rendered on the ticket: the status comment links the
	// URL and says nothing about a pull request's title or body.
	Title string
	Body  string
}

// Outcome is how a WorkTicket run ended.
type Outcome string

const (
	// OutcomeProposed means a pull request is open. This is the only success.
	OutcomeProposed Outcome = "proposed"
	// OutcomeBlocked means the run decided it could not do the ticket and said
	// so on the issue. Not an error: a machine declining a ticket it does not
	// understand is the system working.
	OutcomeBlocked Outcome = "blocked"
	// OutcomeExhausted means the implement/review loop ran out of turn budget,
	// or found the same failing CI check or the same blocking review finding
	// twice, without ever reaching approval.
	//
	// Distinct from OutcomeBlocked: that one is the agent's own explicit
	// verdict that it cannot do the ticket at all, given directly by implement.
	// This one is the counters' backstop firing on a run that was still trying
	// when its budget or its progress ran out. A human reading the outcome
	// comment should be able to tell those apart rather than see one
	// "abandoned" bucket.
	OutcomeExhausted Outcome = "exhausted"
	// OutcomeFailed means the run broke.
	OutcomeFailed Outcome = "failed"
)

// Proposed reports whether this outcome left a pull request behind.
func (o Outcome) Proposed() bool { return o == OutcomeProposed }

// FailureKind is what the dispatcher needs to know about a failure, and
// nothing more.
//
// Three values because the dispatcher does three different things: pause on a
// dead credential, wait out a rate limit, and carry on for anything else. It is
// not a retry taxonomy — that is ErrPermanent's one bit, translated once into
// Temporal's — but a report from a child run to its dispatcher about whether
// the system is still able to work at all.
type FailureKind string

const (
	// FailureNone means the run did not fail.
	FailureNone FailureKind = ""
	// FailureAuth means a credential is wrong, revoked or under-permissioned.
	// Nothing the dispatcher does next will work, so it pauses.
	FailureAuth FailureKind = "auth"
	// FailureRateLimit means a provider limit was reached. A wait, not a dead
	// system, so it trips the cooldown breaker.
	FailureRateLimit FailureKind = "rate-limit"
	// FailureOther is a failure local to one ticket. The ordinary case, and the
	// dispatcher does nothing about it beyond releasing the slot.
	FailureOther FailureKind = "other"
)

// IsFailure reports whether this kind describes a failure at all.
func (k FailureKind) IsFailure() bool { return k != FailureNone }

// TicketDone is what a WorkTicket run signals its dispatcher with when it
// finishes, however it finishes.
//
// It is how the dispatcher learns a slot is free without polling for it. The
// periodic reconcile is the backstop for a run that died without sending this,
// not the primary path — a signal is immediate and a reconcile is up to one
// poll interval late.
type TicketDone struct {
	Ticket  int
	RunID   string
	Outcome Outcome
	Failure FailureKind

	// Detail is what went wrong, for the dispatcher's log and its status. It is
	// never matched on.
	Detail string
}

// InFlightTicket is one ticket the dispatcher believes is being worked.
//
// Status reports the in-flight set as bare issue numbers, which is what an
// operator wants to read. This is what the dispatcher has to hold to be
// correct: the run ID is what tells a completion report from a superseded run
// apart from the current one, and what the orphan sweep matches a pod against.
type InFlightTicket struct {
	Ticket    int
	RunID     string
	StartedAt time.Time
}

// FinishedTicket is one ticket whose most recent run has just ended, held by
// the dispatcher so the next tick does not re-claim it before GitHub's issue
// index has caught up with a label that run just cleared (#405).
//
// ExpiresAt is a deadline, not a duration-since: it is stamped once, from
// DispatcherTuning.ReclaimCooldown, at the moment the ticket finished, so a
// later tuning change cannot retroactively lengthen or shorten a cooldown
// already recorded — the same reasoning as Breaker.OpenUntil.
type FinishedTicket struct {
	Ticket    int
	ExpiresAt time.Time
}

// TrippedAt returns a breaker open for at least cooldown from now.
//
// At *least*: a second trip never shortens a cooldown already in force. Two
// rate limits in a row are more evidence for waiting, not less, and taking the
// shorter deadline would let a burst of cheap failures talk the dispatcher back
// into the wall.
//
// It is a method on Breaker rather than a field it sets, so the type stays a
// value whose only mutation is producing a new one.
func (b Breaker) TrippedAt(now time.Time, cooldown time.Duration, reason string) Breaker {
	until := now.Add(cooldown)
	if !until.After(b.OpenUntil) {
		return b
	}
	return Breaker{OpenUntil: until, Reason: reason}
}
