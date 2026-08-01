package agentactivities_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/clients/codexresponses"
	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

type readFileInput struct {
	Path string `json:"path" jsonschema_description:"Repository-relative path to read."`
}

type fakeTurner struct {
	request codexresponses.TurnRequest
	result  codexresponses.TurnResult
	err     error
	events  []codexresponses.Event
}

func (turner *fakeTurner) Turn(
	_ context.Context,
	request codexresponses.TurnRequest,
	emit codexresponses.EmitFunc,
) (codexresponses.TurnResult, error) {
	turner.request = request
	for _, event := range turner.events {
		emit(event)
	}
	return turner.result, turner.err
}

func TestModelTurnLoadsConversationAndStoresFinalText(t *testing.T) {
	t.Parallel()

	blobStore := blobs.NewMemStore()
	conversations := agent.NewConversationStore(blobStore)
	conversationRef, err := conversations.Append(t.Context(), "agent/run-7/plan", nil, []agent.ConversationItem{
		{Kind: agent.ItemInstructions, Text: "Work carefully."},
		{Kind: agent.ItemUserText, Text: "Design the change."},
	})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	responseSchema := []byte(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`)
	responseSchemaRef, err := agent.NewArtifactStore(blobStore).StoreResponseSchema(t.Context(), "agent/run-7/plan", responseSchema)
	if err != nil {
		t.Fatalf("StoreResponseSchema() error = %v", err)
	}
	transcriptRef, err := agent.NewTranscriptStore(blobStore).Append(t.Context(), "agent/run-7/plan", nil, agent.TranscriptEvent{Type: agent.EventWorkflowPrepared})
	if err != nil {
		t.Fatalf("start transcript: %v", err)
	}
	turner := &fakeTurner{result: codexresponses.TurnResult{
		Outcome: codexresponses.OutcomeFinalText,
		Text:    `{"summary":"done"}`,
		Usage:   codexresponses.Usage{InputTokens: 12, OutputTokens: 3},
	}}
	registry := prometheus.NewRegistry()
	metrics := telemetry.NewMetrics(registry)
	activities, err := agentactivities.NewObservedActivities(
		turner,
		blobStore,
		clocktest.NewFake(time.Unix(0, 0)),
		metrics,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		agenttool.MustSet("coding-read-v1"),
	)
	if err != nil {
		t.Fatalf("NewActivities() error = %v", err)
	}

	result, err := activities.ModelTurn(t.Context(), agent.ModelTurnInput{
		Model:           work.Model{Name: "gpt-test", Effort: "medium"},
		ToolsetID:       "coding-read-v1",
		ConversationRef: conversationRef,
		TranscriptRef:   transcriptRef,
		ResponseFormat:  agent.ResponseFormatRef{Name: "plan_result", SchemaRef: responseSchemaRef},
		PromptCacheKey:  "run-7-plan",
		ModelTurn:       1,
		IdempotencyKey:  "agent/run-7/plan/model/1",
	})
	if err != nil {
		t.Fatalf("ModelTurn() error = %v", err)
	}
	if turner.request.Instructions != "Work carefully." || len(turner.request.Input) != 1 ||
		turner.request.Input[0].Text != "Design the change." || turner.request.Model != "gpt-test" {
		t.Fatalf("Turn() request = %#v", turner.request)
	}
	if turner.request.Store || turner.request.ParallelToolCalls {
		t.Fatalf("Turn() request enables provider storage or parallel tools: %#v", turner.request)
	}
	if turner.request.IdempotencyKey != "agent/run-7/plan/model/1" {
		t.Fatalf("Turn() idempotency key = %q", turner.request.IdempotencyKey)
	}
	if turner.request.ResponseFormat == nil || turner.request.ResponseFormat.Name != "plan_result" ||
		string(turner.request.ResponseFormat.Schema) != string(responseSchema) {
		t.Fatalf("Turn() response format = %#v", turner.request.ResponseFormat)
	}
	if result.Outcome != agent.OutcomeFinalText || result.ConversationRef.Revision != 1 || result.ToolsetFingerprint == "" {
		t.Fatalf("ModelTurn() result = %#v", result)
	}
	if got, err := testutil.GatherAndCount(registry, "software_factory_agent_model_turns_total"); err != nil || got != 1 {
		t.Fatalf("agent model turn metric families = %d, %v; want 1", got, err)
	}
	if result.TranscriptRef.Revision != 1 {
		t.Fatalf("ModelTurn() transcript ref = %#v", result.TranscriptRef)
	}
	if result.Usage != (work.Usage{InputTokens: 12, OutputTokens: 3}) || !result.UsageMeasured {
		t.Fatalf("ModelTurn() usage = %#v, measured = %t", result.Usage, result.UsageMeasured)
	}
	text, err := agent.NewArtifactStore(blobStore).LoadText(t.Context(), result.FinalTextRef)
	if err != nil {
		t.Fatalf("LoadText() error = %v", err)
	}
	if text != `{"summary":"done"}` {
		t.Fatalf("LoadText() = %q", text)
	}
}

func TestModelTurnRejectsPinnedToolsetFingerprintMismatch(t *testing.T) {
	t.Parallel()

	turner := &fakeTurner{}
	activities, err := agentactivities.NewActivities(
		turner, blobs.NewMemStore(), clocktest.NewFake(time.Unix(0, 0)),
		agenttool.MustSet("coding-read-v1"),
	)
	if err != nil {
		t.Fatalf("NewActivities() error = %v", err)
	}
	_, err = activities.ModelTurn(t.Context(), agent.ModelTurnInput{
		ToolsetID: "coding-read-v1", ToolsetFingerprint: "sha256:stale",
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint changed") {
		t.Fatalf("ModelTurn() error = %v, want fingerprint mismatch", err)
	}
	if turner.request.Model != "" {
		t.Fatal("fingerprint mismatch reached the provider")
	}
}

func TestRecordLifecycleEmitsTerminalMetrics(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	activities, err := agentactivities.NewObservedActivities(
		&fakeTurner{}, blobs.NewMemStore(), clocktest.NewFake(time.Unix(0, 0)), telemetry.NewMetrics(registry),
		slog.New(slog.NewTextHandler(io.Discard, nil)), agenttool.MustSet("coding-read-v1"),
	)
	if err != nil {
		t.Fatalf("NewObservedActivities() error = %v", err)
	}
	if err := activities.RecordLifecycle(t.Context(), agentactivities.LifecycleInput{
		Outcome: telemetry.AgentOutcomeCancelled,
	}); err != nil {
		t.Fatalf("RecordLifecycle() error = %v", err)
	}
	got, err := testutil.GatherAndCount(registry, "software_factory_agent_child_outcomes_total")
	if err != nil || got != 1 {
		t.Fatalf("lifecycle metric families = %d, %v; want 1", got, err)
	}
}

func TestModelTurnStoresToolArgumentsAndPreservesCallIDs(t *testing.T) {
	t.Parallel()

	blobStore := blobs.NewMemStore()
	conversations := agent.NewConversationStore(blobStore)
	conversationRef, err := conversations.Append(t.Context(), "agent/run-7/implement/1", nil, []agent.ConversationItem{
		{Kind: agent.ItemInstructions, Text: "Edit carefully."},
		{Kind: agent.ItemUserText, Text: "Implement the change."},
	})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	arguments := []byte(`{"path":"internal/work/work.go"}`)
	turner := &fakeTurner{result: codexresponses.TurnResult{
		Outcome: codexresponses.OutcomeToolCalls,
		ToolCalls: []codexresponses.ToolCall{{
			ID: "fc_1", CallID: "call_1", Name: "read_file", Arguments: arguments,
		}},
		Usage: codexresponses.Usage{InputTokens: 20, OutputTokens: 4},
	}}
	readFile := agenttool.Bind(
		agenttool.Define[readFileInput]("read_file", "Read one repository file."),
		func(_ context.Context, _ readFileInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)
	activities, err := agentactivities.NewActivities(
		turner,
		blobStore,
		clocktest.NewFake(time.Unix(0, 0)),
		agenttool.MustSet("coding-write-v1", readFile),
	)
	if err != nil {
		t.Fatalf("NewActivities() error = %v", err)
	}

	result, err := activities.ModelTurn(t.Context(), agent.ModelTurnInput{
		Model:           work.Model{Name: "gpt-test", Effort: "medium"},
		ToolsetID:       "coding-write-v1",
		ConversationRef: conversationRef,
		PromptCacheKey:  "run-7-implement-1",
		ModelTurn:       1,
		IdempotencyKey:  "agent/run-7/implement/1/model/1",
	})
	if err != nil {
		t.Fatalf("ModelTurn() error = %v", err)
	}
	if result.Outcome != agent.OutcomeToolCalls || len(result.ToolCalls) != 1 {
		t.Fatalf("ModelTurn() result = %#v", result)
	}
	pending := result.ToolCalls[0]
	if pending.CallID != "call_1" || pending.Name != "read_file" {
		t.Fatalf("pending tool call = %#v", pending)
	}
	storedArguments, err := agent.NewArtifactStore(blobStore).LoadArguments(t.Context(), pending.ArgumentsRef)
	if err != nil {
		t.Fatalf("LoadArguments() error = %v", err)
	}
	if !reflect.DeepEqual(storedArguments, arguments) {
		t.Fatalf("LoadArguments() = %s, want %s", storedArguments, arguments)
	}
	items, err := conversations.Items(t.Context(), result.ConversationRef)
	if err != nil {
		t.Fatalf("Items() error = %v", err)
	}
	last := items[len(items)-1]
	if last.Kind != agent.ItemFunctionCall || last.ID != "fc_1" || last.CallID != "call_1" ||
		last.Name != "read_file" || !reflect.DeepEqual([]byte(last.Arguments), arguments) {
		t.Fatalf("stored function call = %#v", last)
	}
}

func TestModelTurnRejectsIncompleteProviderOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result codexresponses.TurnResult
	}{
		{name: "blank final text", result: codexresponses.TurnResult{Outcome: codexresponses.OutcomeFinalText}},
		{name: "blank provider item id", result: codexresponses.TurnResult{
			Outcome:   codexresponses.OutcomeToolCalls,
			ToolCalls: []codexresponses.ToolCall{{CallID: "call_1", Name: "read_file", Arguments: []byte(`{}`)}},
		}},
		{name: "blank call id", result: codexresponses.TurnResult{
			Outcome:   codexresponses.OutcomeToolCalls,
			ToolCalls: []codexresponses.ToolCall{{ID: "fc_1", Name: "read_file", Arguments: []byte(`{}`)}},
		}},
		{name: "blank name", result: codexresponses.TurnResult{
			Outcome:   codexresponses.OutcomeToolCalls,
			ToolCalls: []codexresponses.ToolCall{{ID: "fc_1", CallID: "call_1", Arguments: []byte(`{}`)}},
		}},
		{name: "invalid arguments", result: codexresponses.TurnResult{
			Outcome:   codexresponses.OutcomeToolCalls,
			ToolCalls: []codexresponses.ToolCall{{ID: "fc_1", CallID: "call_1", Name: "read_file", Arguments: []byte(`{`)}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blobStore := blobs.NewMemStore()
			conversations := agent.NewConversationStore(blobStore)
			conversationRef, err := conversations.Append(t.Context(), "agent/run-invalid/plan", nil, []agent.ConversationItem{
				{Kind: agent.ItemInstructions, Text: "Work carefully."},
				{Kind: agent.ItemUserText, Text: "Design the change."},
			})
			if err != nil {
				t.Fatalf("Append() error = %v", err)
			}
			activities, err := agentactivities.NewActivities(
				&fakeTurner{result: test.result},
				blobStore,
				clocktest.NewFake(time.Unix(0, 0)),
				agenttool.MustSet("coding-read-v1"),
			)
			if err != nil {
				t.Fatalf("NewActivities() error = %v", err)
			}

			_, err = activities.ModelTurn(t.Context(), agent.ModelTurnInput{
				Model: work.Model{Name: "gpt-test", Effort: "medium"}, ToolsetID: "coding-read-v1",
				ConversationRef: conversationRef, IdempotencyKey: "agent/run-invalid/plan/model/1",
			})
			var applicationError *temporal.ApplicationError
			if !errors.As(err, &applicationError) || applicationError.Type() != agent.ErrorTypeInvalidProviderOutcome ||
				!applicationError.NonRetryable() {
				t.Fatalf("ModelTurn() error = %T %v, want non-retryable %q", err, err, agent.ErrorTypeInvalidProviderOutcome)
			}
		})
	}
}

func TestModelTurnHeartbeatsContentFreeProgressAndClassifiesProviderErrors(t *testing.T) {
	t.Parallel()

	blobStore := blobs.NewMemStore()
	conversations := agent.NewConversationStore(blobStore)
	conversationRef, err := conversations.Append(t.Context(), "agent/run-progress/plan", nil, []agent.ConversationItem{
		{Kind: agent.ItemInstructions, Text: "Work carefully."},
		{Kind: agent.ItemUserText, Text: "Design the change."},
	})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	turner := &fakeTurner{
		result: codexresponses.TurnResult{Outcome: codexresponses.OutcomeFinalText, Text: `{"summary":"done"}`},
		events: []codexresponses.Event{
			{Type: codexresponses.EventReasoningDelta, Delta: "private reasoning must not persist"},
			{Type: codexresponses.EventTextDelta, Delta: "secret response chunk"},
		},
	}
	activities, err := agentactivities.NewActivities(
		turner, blobStore, clocktest.NewFake(time.Unix(0, 0)), agenttool.MustSet("coding-read-v1"),
	)
	if err != nil {
		t.Fatalf("NewActivities() error = %v", err)
	}
	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.ModelTurn)
	var heartbeats []agentactivities.StreamProgress
	environment.SetOnActivityHeartbeatListener(func(_ *activity.Info, details converter.EncodedValues) {
		var progress agentactivities.StreamProgress
		if err := details.Get(&progress); err != nil {
			t.Errorf("heartbeat decode error = %v", err)
			return
		}
		heartbeats = append(heartbeats, progress)
	})
	_, err = environment.ExecuteActivity(activities.ModelTurn, agent.ModelTurnInput{
		Model: work.Model{Name: "gpt-test", Effort: "medium"}, ToolsetID: "coding-read-v1",
		ConversationRef: conversationRef, IdempotencyKey: "agent/run-progress/plan/model/1",
	})
	if err != nil {
		t.Fatalf("ExecuteActivity() error = %v", err)
	}
	heartbeatJSON, err := json.Marshal(heartbeats)
	if err != nil {
		t.Fatalf("Marshal(heartbeats) error = %v", err)
	}
	if len(heartbeats) == 0 || bytes.Contains(heartbeatJSON, []byte("private reasoning")) ||
		bytes.Contains(heartbeatJSON, []byte("secret response")) {
		t.Fatalf("heartbeats contain content or are absent: %s", heartbeatJSON)
	}
	turner.events = nil

	tests := []struct {
		name             string
		toolsetID        agent.ToolsetID
		providerError    error
		wantType         string
		wantNonRetryable bool
	}{
		{
			name: "rate limit", toolsetID: "coding-read-v1", providerError: codexresponses.ErrRateLimited,
			wantType: agent.ErrorTypeRateLimit, wantNonRetryable: true,
		},
		{
			name: "provider auth", toolsetID: "coding-read-v1", providerError: codexresponses.ErrAuth,
			wantType: agent.ErrorTypeAuth, wantNonRetryable: true,
		},
		{
			name: "stream interruption", toolsetID: "coding-read-v1", providerError: codexresponses.ErrStreamInterrupted,
			wantType: agent.ErrorTypeTransient,
		},
		{name: "unknown toolset", toolsetID: "unknown-v1", wantType: agent.ErrorTypeInvalidInput, wantNonRetryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			turner.err = test.providerError
			_, err := activities.ModelTurn(t.Context(), agent.ModelTurnInput{
				Model: work.Model{Name: "gpt-test", Effort: "medium"}, ToolsetID: test.toolsetID,
				ConversationRef: conversationRef, IdempotencyKey: "agent/run-progress/plan/model/2",
			})
			var applicationError *temporal.ApplicationError
			if !errors.As(err, &applicationError) || applicationError.Type() != test.wantType ||
				applicationError.NonRetryable() != test.wantNonRetryable {
				t.Fatalf("ModelTurn() error = %T %v, want type %q non-retryable %t",
					err, err, test.wantType, test.wantNonRetryable)
			}
		})
	}
}
