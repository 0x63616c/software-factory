package store_test

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// measuredAttempt returns an Attempt that actually ran, with the given usage.
func measuredAttempt(attemptNo int, usage work.Usage) store.Attempt {
	return store.Attempt{AttemptNo: attemptNo, Usage: usage, Measured: true}
}

// resumedAttempt returns an Attempt that resumed a stored result without
// running Codex — ADR-0012's third case, zero tokens and Measured false.
func resumedAttempt(attemptNo int) store.Attempt {
	return store.Attempt{AttemptNo: attemptNo, Measured: false}
}

func TestStepUsageSumsEveryMeasuredAttempt(t *testing.T) {
	t.Parallel()
	step := store.StepDetail{Attempts: []store.Attempt{
		measuredAttempt(1, work.Usage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 50, ReasoningTokens: 10}),
		measuredAttempt(2, work.Usage{InputTokens: 200, CachedInputTokens: 0, OutputTokens: 30, ReasoningTokens: 5}),
	}}

	usage, complete := step.Usage()

	if !complete {
		t.Fatalf("Usage() complete = false, want true: every attempt was measured")
	}
	want := work.Usage{InputTokens: 300, CachedInputTokens: 20, OutputTokens: 80, ReasoningTokens: 15}
	if usage != want {
		t.Fatalf("Usage() = %+v, want %+v", usage, want)
	}
}

// TestStepUsageIsIncompleteWhenAnAttemptResumed is the test for ADR-0012's
// "one thing that must not be got wrong": a resumed Attempt (#426) must make
// the Step's own total incomplete rather than a confident sum that silently
// drops it. Reverting rollup.go's `continue` to instead add the resumed
// attempt's (zero) Usage and never flip complete to false makes this test
// fail with complete = true — that failure is the evidence this behaviour is
// actually being tested, not merely typed.
func TestStepUsageIsIncompleteWhenAnAttemptResumed(t *testing.T) {
	t.Parallel()
	step := store.StepDetail{Attempts: []store.Attempt{
		measuredAttempt(1, work.Usage{InputTokens: 100, OutputTokens: 50}),
		resumedAttempt(2),
	}}

	usage, complete := step.Usage()

	if complete {
		t.Fatalf("Usage() complete = true, want false: attempt 2 was never measured")
	}
	// The measured attempt's real spend must still be visible — "incomplete"
	// describes the total's trustworthiness, not a reason to hide it.
	want := work.Usage{InputTokens: 100, OutputTokens: 50}
	if usage != want {
		t.Fatalf("Usage() = %+v, want %+v (the one measured attempt's usage)", usage, want)
	}
}

func TestRunUsageIsIncompleteWhenAnyStepIsIncomplete(t *testing.T) {
	t.Parallel()
	detail := store.RunDetail{Steps: []store.StepDetail{
		{Stage: work.StagePlan, Turn: 1, Attempts: []store.Attempt{
			measuredAttempt(1, work.Usage{InputTokens: 10}),
		}},
		{Stage: work.StageImplement, Turn: 1, Attempts: []store.Attempt{
			measuredAttempt(1, work.Usage{InputTokens: 20}),
			resumedAttempt(2),
		}},
	}}

	usage, complete := detail.Usage()

	if complete {
		t.Fatalf("Usage() complete = true, want false: the implement step has a resumed attempt")
	}
	want := work.Usage{InputTokens: 30}
	if usage != want {
		t.Fatalf("Usage() = %+v, want %+v", usage, want)
	}
}

func TestRunUsageIsCompleteWhenEveryAttemptWasMeasured(t *testing.T) {
	t.Parallel()
	detail := store.RunDetail{Steps: []store.StepDetail{
		{Stage: work.StagePlan, Turn: 1, Attempts: []store.Attempt{measuredAttempt(1, work.Usage{InputTokens: 5})}},
	}}

	_, complete := detail.Usage()

	if !complete {
		t.Fatalf("Usage() complete = false, want true: nothing resumed")
	}
}
