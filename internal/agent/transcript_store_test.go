package agent_test

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/blobs"
)

func TestTranscriptStoreAppendsImmutableProviderNeutralEvents(t *testing.T) {
	t.Parallel()

	store := agent.NewTranscriptStore(blobs.NewMemStore())
	prepared, err := store.Append(t.Context(), "agent/run-7/implement/1", nil, agent.TranscriptEvent{Type: agent.EventWorkflowPrepared})
	if err != nil {
		t.Fatalf("Append(prepared) error = %v", err)
	}
	completed, err := store.Append(t.Context(), "agent/run-7/implement/1", &prepared, agent.TranscriptEvent{
		Type: agent.EventModelCompleted, ModelTurn: 1, Outcome: string(agent.OutcomeToolCalls), DurationMillis: 25,
	})
	if err != nil {
		t.Fatalf("Append(completed) error = %v", err)
	}
	if prepared.Revision != 0 || completed.Revision != 1 || completed.Bytes <= prepared.Bytes {
		t.Fatalf("refs prepared=%#v completed=%#v", prepared, completed)
	}
	events, err := store.Events(t.Context(), completed)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 2 || events[0].Type != agent.EventWorkflowPrepared || events[1].ModelTurn != 1 {
		t.Fatalf("Events() = %#v", events)
	}
	jsonl, err := store.JSONL(t.Context(), completed)
	if err != nil {
		t.Fatalf("JSONL() error = %v", err)
	}
	if strings.Count(string(jsonl), "\n") != 2 || !strings.Contains(string(jsonl), `"type":"model_completed"`) {
		t.Fatalf("JSONL() = %q", jsonl)
	}
}
