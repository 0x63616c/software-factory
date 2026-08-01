package work_test

import (
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestDefaultDispatcherTuningValidates(t *testing.T) {
	t.Parallel()

	if err := work.DefaultDispatcherTuning().Validate(); err != nil {
		t.Fatalf("the default tuning must be usable as shipped: %v", err)
	}
}

func TestDispatcherTuningRejectsValuesTheLoopCannotRunOn(t *testing.T) {
	t.Parallel()

	cases := map[string]func(c *work.DispatcherTuning){
		"no history ceiling": func(c *work.DispatcherTuning) { c.MaxHistoryEvents = 0 },
	}

	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tuning := work.DefaultDispatcherTuning()
			breakIt(&tuning)
			if err := tuning.Validate(); err == nil {
				t.Fatal("an unusable tuning must fail loudly")
			}
		})
	}
}

func TestBreakerIsClosedUntilItIsTripped(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	var breaker work.Breaker
	if breaker.OpenAt(now) {
		t.Fatal("the zero breaker is closed")
	}

	tripped := breaker.TrippedAt(now, time.Minute, "github asked us to wait")
	if !tripped.OpenAt(now) {
		t.Fatal("a tripped breaker is open")
	}
	if tripped.OpenAt(now.Add(time.Minute)) {
		t.Fatal("the breaker closes the instant its cooldown elapses")
	}
	if breaker.OpenAt(now) {
		t.Fatal("TrippedAt must not mutate the breaker it was called on")
	}
}

func TestBreakerKeepsTheLaterDeadlineWhenItIsTrippedTwice(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	long := work.Breaker{}.TrippedAt(now, 10*time.Minute, "long")
	short := long.TrippedAt(now, time.Minute, "short")

	if !short.OpenAt(now.Add(5 * time.Minute)) {
		t.Fatal("a shorter second trip must not shorten a cooldown already in force")
	}
	if short.Reason != "long" {
		t.Fatalf("reason = %q, want the reason of the trip still in force", short.Reason)
	}
}

func TestFailureKindNamesTheKindsTheDispatcherActsOn(t *testing.T) {
	t.Parallel()

	if work.FailureNone.IsFailure() {
		t.Fatal("the absence of a failure is not a failure")
	}
	if !work.FailureAuth.IsFailure() || !work.FailureRateLimit.IsFailure() || !work.FailureOther.IsFailure() {
		t.Fatal("every other kind is")
	}
}

func TestOutcomeSaysWhetherAPullRequestExists(t *testing.T) {
	t.Parallel()

	if !work.OutcomeProposed.Proposed() {
		t.Fatal("proposed means a PR is open")
	}
	if work.OutcomeBlocked.Proposed() || work.OutcomeFailed.Proposed() {
		t.Fatal("nothing else does")
	}
}
