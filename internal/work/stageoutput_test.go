package work

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNewStageOutputRoundTripsThroughJSON(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		stage Stage
		value stageOutputValue
	}{
		{name: "plan", stage: StagePlan, value: DocumentOutput{Document: "the plan"}},
		{
			name:  "review",
			stage: StageReview,
			value: ReviewOutput{Document: "the review", Findings: []Finding{{ID: "f1", Blocking: true, Summary: "s"}}},
		},
		{
			name:  "implement",
			stage: StageImplement,
			value: ImplementOutput{
				Report: "did the work", Blocked: true, BlockedReason: "needs a human",
				Title: "t", Body: "b",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			original := NewStageOutput(tc.stage, tc.value)
			data, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var got StageOutput
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Stage() != tc.stage {
				t.Errorf("Stage() = %q, want %q", got.Stage(), tc.stage)
			}
			if !reflect.DeepEqual(got.Value(), tc.value) {
				t.Errorf("Value() = %#v, want %#v", got.Value(), tc.value)
			}
			if got.Prose() != tc.value.Prose() {
				t.Errorf("Prose() = %q, want %q", got.Prose(), tc.value.Prose())
			}
		})
	}
}

func TestNewStageOutputPanicsOnAMismatchedShape(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewStageOutput(StagePlan, ImplementOutput{...}) did not panic")
		}
		if !strings.Contains(r.(string), "does not produce") {
			t.Errorf("panic value = %v, want it to say the stage does not produce that shape", r)
		}
	}()
	NewStageOutput(StagePlan, ImplementOutput{Report: "x"})
}

func TestZeroStageOutputProseIsEmptyRatherThanPanicking(t *testing.T) {
	t.Parallel()

	var zero StageOutput
	if got := zero.Prose(); got != "" {
		t.Errorf("Prose() of a stage that has not run = %q, want \"\"", got)
	}
}

func TestMarshalJSONRefusesAnEmptyStageOutput(t *testing.T) {
	t.Parallel()

	var zero StageOutput
	if _, err := json.Marshal(zero); err == nil {
		t.Fatal("marshalling a StageOutput with no value succeeded; NewStageOutput was never called")
	}
}

// TestUnmarshalJSONRefusesAJSONObjectCarryingNoStageTag pins the no-default
// switch in decodeStageOutputValue: an object with no recognised "stage" key
// is refused with an explicit error rather than decoding into a StageOutput
// with a nil value, which downstream code would read as "this stage produced
// nothing".
//
// This is NOT the pre-this-step-payload migration guard, despite using a
// payload of that shape as its no-stage-tag example. That migration happens a
// level up, at the activity result, where `Document` was renamed to `Result`
// — a payload from before this step has no "Result" key, so it never reaches
// this method at all. RunStageOutput.UnmarshalJSON is what catches it, and
// activities.TestRunStageOutputRefusesThePreThisStepShape is that guard.
func TestUnmarshalJSONRefusesAJSONObjectCarryingNoStageTag(t *testing.T) {
	t.Parallel()

	oldShape := []byte(`{"Output":"eyJkb2N1bWVudCI6IngifQ==","Document":"x","ThreadID":"thread-1","Usage":{"InputTokens":1}}`)

	var got StageOutput
	err := json.Unmarshal(oldShape, &got)
	if err == nil {
		t.Fatal("UnmarshalJSON accepted the pre-this-step wire shape; want an explicit error")
	}
	if got.Value() != nil {
		t.Errorf("a refused decode left a non-nil value: %#v", got.Value())
	}
}

func TestDecodeStageOutputValueIsExhaustiveOverPipeline(t *testing.T) {
	t.Parallel()

	for _, stage := range Pipeline() {
		t.Run(string(stage), func(t *testing.T) {
			t.Parallel()

			var raw json.RawMessage
			switch stage {
			case StageImplement:
				raw = json.RawMessage(`{"Report":"r","Blocked":false,"BlockedReason":"","Title":"","Body":""}`)
			case StageReview:
				raw = json.RawMessage(`{"Document":"d","Findings":[]}`)
			case StagePlan:
				raw = json.RawMessage(`{"Document":"d"}`)
			}
			if _, err := decodeStageOutputValue(stage, raw); err != nil {
				t.Fatalf("decodeStageOutputValue(%s): %v", stage, err)
			}
		})
	}
}

func TestDecodeStageOutputValueRefusesAStageOutsideThePipeline(t *testing.T) {
	t.Parallel()

	if _, err := decodeStageOutputValue(Stage("summarise"), json.RawMessage(`{}`)); err == nil {
		t.Fatal("decodeStageOutputValue accepted a stage this pipeline does not have")
	}
}
