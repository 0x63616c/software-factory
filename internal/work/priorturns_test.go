package work

import (
	"encoding/json"
	"testing"
)

// TestPriorTurnsMarshalsCleanlyWhenNoStageHasRunYet is the regression this
// type's own doc comment names: StageOutput.MarshalJSON deliberately refuses
// its own zero value, which is exactly what most of a run's early turns leave
// PriorTurns holding in two of its three fields (or all three, on plan's
// turn). A plain struct-field encoding of PriorTurns would hit that guard on
// nearly every real activity input this service ever builds — caught for
// real by TestNewSandboxSideBuildsAWorkingRunPlan panicking the first time
// this type shipped, activity input encoding included the zero PriorTurns a
// plan turn is built with.
func TestPriorTurnsMarshalsCleanlyWhenNoStageHasRunYet(t *testing.T) {
	t.Parallel()

	var zero PriorTurns
	data, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("Marshal(zero PriorTurns): %v", err)
	}

	var got PriorTurns
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Plan.Value() != nil || got.LatestImplement.Value() != nil || got.LatestReview.Value() != nil {
		t.Errorf("round-tripping the zero value produced a non-zero field: %#v", got)
	}
}

// TestPriorTurnsRoundTripsWhateverTurnsHaveActuallyRun proves the common,
// partial case too: a run partway through only has a plan and one implement
// turn, never a review yet, and that mix must round-trip exactly — the
// stages that ran keep their content, and the stage that has not stays the
// zero value.
func TestPriorTurnsRoundTripsWhateverTurnsHaveActuallyRun(t *testing.T) {
	t.Parallel()

	original := PriorTurns{
		Plan:            NewStageOutput(StagePlan, DocumentOutput{Document: "the plan"}),
		LatestImplement: NewStageOutput(StageImplement, ImplementOutput{Report: "turn one's report"}),
		// LatestReview left zero: review has not run yet this run.
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got PriorTurns
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Plan.Prose() != "the plan" {
		t.Errorf("Plan.Prose() = %q, want %q", got.Plan.Prose(), "the plan")
	}
	if got.LatestImplement.Prose() != "turn one's report" {
		t.Errorf("LatestImplement.Prose() = %q, want %q", got.LatestImplement.Prose(), "turn one's report")
	}
	if got.LatestReview.Value() != nil {
		t.Errorf("LatestReview should still be zero (review has not run), got %#v", got.LatestReview.Value())
	}
}
