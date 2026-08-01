package activities

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/clients/github"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/temporal"
)

// permanent builds an error shaped the way the github client builds one: a kind
// and work.ErrPermanent travelling together.
func permanent(kind error) error {
	return fmt.Errorf("clearing the auto label on issue #1: %w (%w): %w", kind, work.ErrPermanent, errors.New("403"))
}

func appErrorOf(t *testing.T, err error) *temporal.ApplicationError {
	t.Helper()
	var app *temporal.ApplicationError
	if !errors.As(err, &app) {
		t.Fatalf("expected an ApplicationError, got %T: %v", err, err)
	}
	return app
}

func TestFailTypesAPermanentAuthFailureSoTheDispatcherCanPauseOnIt(t *testing.T) {
	t.Parallel()

	err := fail(t.Context(), "clearing the auto label", permanent(github.ErrAuth))

	app := appErrorOf(t, err)
	if app.Type() != ErrTypeAuth {
		t.Fatalf("type = %q, want %q — the dispatcher pauses on this type and nothing else", app.Type(), ErrTypeAuth)
	}
	if !app.NonRetryable() {
		t.Fatal("no number of retries mints a permission the installation does not hold")
	}
}

func TestFailTypesEachKindTheClientDistinguishes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind error
		want string
	}{
		{github.ErrAuth, ErrTypeAuth},
		{github.ErrRateLimit, ErrTypeRateLimit},
		{github.ErrNotFound, ErrTypeNotFound},
		{github.ErrInvalid, ErrTypeInvalid},
	}

	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			t.Parallel()
			app := appErrorOf(t, fail(t.Context(), "op", permanent(c.kind)))
			if app.Type() != c.want {
				t.Fatalf("type = %q, want %q", app.Type(), c.want)
			}
		})
	}
}

func TestFailTypesAPermanentFailureWithNoKindAsPermanent(t *testing.T) {
	t.Parallel()

	app := appErrorOf(t, fail(t.Context(), "op", fmt.Errorf("the sandbox never became ready: %w", work.ErrPermanent)))

	if app.Type() != ErrTypePermanent {
		t.Fatalf("type = %q, want %q", app.Type(), ErrTypePermanent)
	}
	if !app.NonRetryable() {
		t.Fatal("work.ErrPermanent is the one bit that means stop paying for attempts")
	}
}

func TestFailLeavesAnUnmarkedFailureRetryable(t *testing.T) {
	t.Parallel()

	app := appErrorOf(t, fail(t.Context(), "op", errors.New("502 bad gateway")))

	if app.NonRetryable() {
		t.Fatal("anything unmarked is retryable — that is Temporal's default and this must not override it")
	}
	if app.Type() != ErrTypeTransient {
		t.Fatalf("type = %q, want %q", app.Type(), ErrTypeTransient)
	}
}

func TestFailKeepsARetryableRateLimitRetryable(t *testing.T) {
	t.Parallel()

	// A secondary limit: the client marks the kind but deliberately not
	// permanence, because it is typically a minute long and failing the
	// activity would discard every token the run has already spent.
	secondary := fmt.Errorf("posting a status comment: %w, github asked us to wait 1m0s", github.ErrRateLimit)

	app := appErrorOf(t, fail(t.Context(), "op", secondary))

	if app.NonRetryable() {
		t.Fatal("a transient rate limit must stay retryable; only the permanent one is fatal")
	}
	if app.Type() != ErrTypeRateLimit {
		t.Fatalf("type = %q, want %q — the type says why, NonRetryable says whether", app.Type(), ErrTypeRateLimit)
	}
}

func TestFailKeepsARepositoryPolicyRejectionTypedAndRetryable(t *testing.T) {
	t.Parallel()

	err := fail(t.Context(), "merging pull request", fmt.Errorf("review required: %w", github.ErrRuleset))
	app := appErrorOf(t, err)

	if app.NonRetryable() {
		t.Fatal("repository policy must remain retryable for bounded operator repair")
	}
	if app.Type() != ErrTypeRuleset {
		t.Fatalf("type = %q, want %q", app.Type(), ErrTypeRuleset)
	}
}

func TestFailReportsCancellationAsCancellationRatherThanAsAFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := fail(ctx, "running the plan stage", errors.New("exec stream closed"))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled activity must surface as cancellation, got %v", err)
	}
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		t.Fatal("cancellation must not be dressed up as an application error, or Temporal retries into a dead context")
	}
}

func TestFailNamesTheOperationItWasDoing(t *testing.T) {
	t.Parallel()

	err := fail(t.Context(), "clearing the auto label on issue #328", permanent(github.ErrAuth))

	if got := err.Error(); !strings.Contains(got, "clearing the auto label on issue #328") {
		t.Fatalf("the message must name the operation, got %q", got)
	}
}

func TestFailPassesNilThrough(t *testing.T) {
	t.Parallel()

	if err := fail(t.Context(), "op", nil); err != nil {
		t.Fatalf("success is not a failure, got %v", err)
	}
}

func TestFailureKindOfReadsBackWhatTheDispatcherActsOn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want work.FailureKind
	}{
		{"nil", nil, work.FailureNone},
		{"auth", fail(t.Context(), "op", permanent(github.ErrAuth)), work.FailureAuth},
		{"rate limit", fail(t.Context(), "op", permanent(github.ErrRateLimit)), work.FailureRateLimit},
		{"not found", fail(t.Context(), "op", permanent(github.ErrNotFound)), work.FailureOther},
		{"transient", fail(t.Context(), "op", errors.New("502")), work.FailureOther},
		{"not an application error", errors.New("plain"), work.FailureOther},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := FailureKindOf(c.err); got != c.want {
				t.Fatalf("FailureKindOf = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFailureKindOfSeesThroughTheWrappingTemporalAddsOnTheWayToAWorkflow(t *testing.T) {
	t.Parallel()

	// An activity failure reaches workflow code wrapped in an ActivityError.
	// Matching only the outermost error would silently classify every auth
	// failure as ordinary, and the dispatcher would never pause.
	wrapped := fmt.Errorf("activity error: %w", fail(t.Context(), "op", permanent(github.ErrAuth)))

	if got := FailureKindOf(wrapped); got != work.FailureAuth {
		t.Fatalf("FailureKindOf = %q, want %q", got, work.FailureAuth)
	}
}

func TestThePermanentTypeStringIsTheOneWorkflowRetryPoliciesArePinnedTo(t *testing.T) {
	t.Parallel()

	// Asserted as a literal, not against the constant. A rename would otherwise
	// move both sides together and silently turn every permanent failure into
	// an infinite retry, because a NonRetryableErrorTypes entry that matches
	// nothing is a policy that retries everything.
	if ErrTypePermanent != "PermanentFailure" {
		t.Fatalf("ErrTypePermanent = %q, want %q", ErrTypePermanent, "PermanentFailure")
	}
}

func TestFailTreatsACancellationWrappedInAPermanentErrorAsCancellation(t *testing.T) {
	t.Parallel()

	// A draining worker cancels its activities on SIGTERM, and a client may
	// well wrap that cancellation in its own permanent error on the way out.
	// Reported as a permanent application failure, the ticket fails on every
	// deploy and is never picked up again.
	wrapped := fmt.Errorf("running the plan stage: %w: %w", work.ErrPermanent, context.Canceled)

	err := fail(t.Context(), "op", wrapped)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled drain must surface as cancellation, got %v", err)
	}
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		t.Fatal("and never as a non-retryable application error")
	}
}
