package agentactivities_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type countedInput struct {
	Value string `json:"value" jsonschema_description:"Value returned by the test tool."`
}

func TestToolRetryReturnsTheRecordedResultWithoutExecutingTwice(t *testing.T) {
	t.Parallel()

	blobStore := blobs.NewMemStore()
	conversations := agent.NewConversationStore(blobStore)
	initial, err := conversations.Append(t.Context(), "agent/run-7/implement/1", nil, []agent.ConversationItem{{
		Kind: agent.ItemUserText, Text: "Use the tool.",
	}})
	if err != nil {
		t.Fatalf("Append(initial) error = %v", err)
	}
	arguments := []byte(`{"value":"once"}`)
	requested, err := conversations.Append(t.Context(), "agent/run-7/implement/1", &initial, []agent.ConversationItem{{
		Kind: agent.ItemFunctionCall, ID: "fc_1", CallID: "call_1", Name: "counted", Arguments: arguments,
	}})
	if err != nil {
		t.Fatalf("Append(requested) error = %v", err)
	}
	argumentsRef, err := agent.NewArtifactStore(blobStore).StoreArguments(
		t.Context(), "agent/run-7/implement/1", arguments,
	)
	if err != nil {
		t.Fatalf("StoreArguments() error = %v", err)
	}
	transcriptRef, err := agent.NewTranscriptStore(blobStore).Append(
		t.Context(), "agent/run-7/implement/1", nil, agent.TranscriptEvent{Type: agent.EventWorkflowPrepared},
	)
	if err != nil {
		t.Fatalf("start transcript: %v", err)
	}
	executions := 0
	tool := agenttool.Bind(
		agenttool.Define[countedInput]("counted", "Return one value while counting executions."),
		func(_ context.Context, input countedInput) (agenttool.Result, error) {
			executions++
			return agenttool.Result{Content: input.Value}, nil
		},
	)
	registry := prometheus.NewRegistry()
	toolset := agenttool.MustSet("coding-write-v1", tool)
	activities, err := agentactivities.NewObservedToolActivities(
		blobStore,
		clocktest.NewFake(time.Unix(0, 0)),
		telemetry.NewMetrics(registry),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		toolset,
	)
	if err != nil {
		t.Fatalf("NewToolActivities() error = %v", err)
	}
	input := agent.ToolInput{
		ToolsetID:          "coding-write-v1",
		ToolsetFingerprint: toolset.Fingerprint(),
		ConversationRef:    requested,
		TranscriptRef:      transcriptRef,
		Call:               agent.PendingToolCall{CallID: "call_1", Name: "counted", ArgumentsRef: argumentsRef},
	}
	first, err := activities.Tool(t.Context(), input)
	if err != nil {
		t.Fatalf("first Tool() error = %v", err)
	}
	second, err := activities.Tool(t.Context(), input)
	if err != nil {
		t.Fatalf("second Tool() error = %v", err)
	}
	if executions != 1 {
		t.Fatalf("tool executions = %d, want 1", executions)
	}
	if got, err := testutil.GatherAndCount(registry, "software_factory_agent_tool_calls_total"); err != nil || got != 1 {
		t.Fatalf("agent tool metric families = %d, %v; want 1", got, err)
	}
	if first != second || first.CallID != "call_1" || first.ConversationRef.Revision != 2 {
		t.Fatalf("tool results = %#v and %#v", first, second)
	}
	if first.TranscriptRef.Revision != 1 {
		t.Fatalf("tool transcript ref = %#v", first.TranscriptRef)
	}
	items, err := conversations.Items(t.Context(), first.ConversationRef)
	if err != nil {
		t.Fatalf("Items() error = %v", err)
	}
	if last := items[len(items)-1]; last.Kind != agent.ItemFunctionOutput || last.CallID != "call_1" || last.Output != "once" {
		t.Fatalf("stored tool output = %#v", last)
	}
}

func TestToolRejectsPinnedToolsetFingerprintMismatch(t *testing.T) {
	t.Parallel()

	toolset := agenttool.MustSet("coding-write-v1")
	activities, err := agentactivities.NewToolActivities(
		blobs.NewMemStore(), clocktest.NewFake(time.Unix(0, 0)), toolset,
	)
	if err != nil {
		t.Fatalf("NewToolActivities() error = %v", err)
	}
	_, err = activities.Tool(t.Context(), agent.ToolInput{
		ToolsetID: "coding-write-v1", ToolsetFingerprint: "sha256:stale",
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint changed") {
		t.Fatalf("Tool() error = %v, want fingerprint mismatch", err)
	}
}
