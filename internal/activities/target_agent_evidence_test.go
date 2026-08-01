package activities

import (
	"encoding/json"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/work"
)

func TestTargetAgentEvidenceInputRoundTripsFailedEvidenceWithoutAResult(t *testing.T) {
	t.Parallel()
	want := TargetAgentEvidenceInput{
		AttemptID:   store.TargetAttemptID{RunID: "019fb901-0000-7000-8000-000000000002", StepOrdinal: 1, AttemptNo: 1},
		Identity:    "agent/019fb901-0000-7000-8000-000000000002/step/1/attempt/1",
		State:       work.AgentAttemptFailed,
		FailureKind: work.RunFailureAgentUnrecoverable,
		EndedAt:     fixedTestTime,
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal failed target evidence: %v", err)
	}
	var got TargetAgentEvidenceInput
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal failed target evidence: %v", err)
	}
	if got.Result != nil || got.State != want.State || got.FailureKind != want.FailureKind || got.AttemptID != want.AttemptID || got.Identity != want.Identity || !got.EndedAt.Equal(want.EndedAt) {
		t.Fatalf("failed evidence round trip = %+v, want %#v", got, want)
	}
}

func TestTargetAgentEvidenceRequiresAnExplicitWellFormedState(t *testing.T) {
	t.Parallel()
	activities, err := NewTargetAgentEvidenceActivities(storefake.New(), blobs.NewMemStore())
	if err != nil {
		t.Fatalf("NewTargetAgentEvidenceActivities: %v", err)
	}
	base := TargetAgentEvidenceInput{
		AttemptID: store.TargetAttemptID{RunID: "019fb901-0000-7000-8000-000000000001", StepOrdinal: 1, AttemptNo: 1},
		Identity:  "agent/019fb901-0000-7000-8000-000000000001/step/1/attempt/1",
		EndedAt:   fixedTestTime,
	}
	for _, test := range []struct {
		name  string
		input TargetAgentEvidenceInput
	}{
		{name: "missing state", input: base},
		{name: "blank failed reason", input: func() TargetAgentEvidenceInput { value := base; value.State = work.AgentAttemptFailed; return value }()},
		{name: "running terminal", input: func() TargetAgentEvidenceInput { value := base; value.State = work.AgentAttemptRunning; return value }()},
		{name: "failed result", input: func() TargetAgentEvidenceInput {
			value := base
			value.State, value.FailureKind = work.AgentAttemptFailed, work.RunFailureAgentUnrecoverable
			result := work.NewStageOutput(work.StagePlan, work.DocumentOutput{Document: "not allowed"})
			value.Result = &result
			return value
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := activities.Finalize(t.Context(), test.input); err == nil {
				t.Fatal("Finalize() succeeded, want invalid evidence failure")
			}
		})
	}
}
