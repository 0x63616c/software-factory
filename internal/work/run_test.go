package work_test

import (
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestDefaultRunPolicyValidates(t *testing.T) {
	t.Parallel()

	if err := work.DefaultRunPolicy().Validate(); err != nil {
		t.Fatalf("the default policy must be usable as shipped: %v", err)
	}
}

func TestRunPolicyRejectsAnIncompletePolicyRatherThanDefaultingIt(t *testing.T) {
	t.Parallel()

	cases := map[string]func(p *work.RunPolicy){
		"no stage timeout":     func(p *work.RunPolicy) { p.StageTimeout = 0 },
		"no heartbeat timeout": func(p *work.RunPolicy) { p.StageHeartbeatTimeout = 0 },
		"no run timeout":       func(p *work.RunPolicy) { p.RunTimeout = 0 },
		"no stage attempts":    func(p *work.RunPolicy) { p.StageAttempts = 0 },
		"no control timeout":   func(p *work.RunPolicy) { p.ControlTimeout = 0 },
		"no control attempts":  func(p *work.RunPolicy) { p.ControlAttempts = 0 },
	}

	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			policy := work.DefaultRunPolicy()
			breakIt(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("an incomplete policy must fail loudly, not acquire a second default")
			}
		})
	}
}

func TestRunPolicyRejectsAHeartbeatTimeoutAboveTheStageTimeout(t *testing.T) {
	t.Parallel()

	policy := work.DefaultRunPolicy()
	policy.StageHeartbeatTimeout = policy.StageTimeout + time.Minute

	if err := policy.Validate(); err == nil {
		t.Fatal("a heartbeat timeout above the stage timeout can never fire, so it must be refused")
	}
}

func TestARunIsGivenLongerThanItsStagesCanTake(t *testing.T) {
	t.Parallel()

	policy := work.DefaultRunPolicy()

	if policy.RunTimeout <= policy.RunBudget() {
		t.Fatalf("run timeout %s does not exceed the stages' budget %s, so a run using its stage timeouts "+
			"would be killed for taking exactly as long as it was allowed", policy.RunTimeout, policy.RunBudget())
	}
	// Not len(work.Pipeline()) (3): a run's real worst case is the
	// implement/review loop's derived ceiling of MaxStageInvocations (19)
	// stage invocations, not one pass over the three stages Pipeline() names.
	if want := policy.StageTimeout * time.Duration(work.MaxStageInvocations); policy.RunBudget() != want {
		t.Fatalf("run budget = %s, want every invocation's timeout summed (%s)", policy.RunBudget(), want)
	}
}

func TestRunPolicyRefusesARunTimeoutSizedForTheOldFiveStageFormula(t *testing.T) {
	t.Parallel()

	// A value that passes the old RunTimeout <= StageTimeout*5 bound but
	// fails the loop's real RunTimeout <= StageTimeout*MaxStageInvocations
	// (19) bound must be rejected — this is the acceptance criterion the
	// pipeline-rewrite spec names for RunPolicy.Validate under slice ii.
	policy := work.DefaultRunPolicy()
	policy.RunTimeout = policy.StageTimeout * 5

	if err := policy.Validate(); err == nil {
		t.Fatal("a run timeout sized for 5 stages must be refused once the loop's real ceiling is 19 invocations")
	}
}

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

func TestRunPolicyRefusesARunTimeoutItsOwnStagesCanExhaust(t *testing.T) {
	t.Parallel()

	policy := work.DefaultRunPolicy()
	policy.RunTimeout = policy.RunBudget()

	if err := policy.Validate(); err == nil {
		t.Fatal("a run whose stages can use every second of its timeout is a run Temporal kills for doing " +
			"exactly what it was allowed to do; the ladder is the invariant, so it is checked")
	}
}
