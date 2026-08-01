package agentactivities_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/0x63616c/software-factory/internal/activities"
	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/prompts"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/temporal"
)

type recordingPromptRenderer struct {
	prompt string
	schema []byte
	key    work.StageKey
	detail work.TicketDetail
	prior  work.PriorTurns
}

func (renderer *recordingPromptRenderer) Render(key work.StageKey, detail work.TicketDetail, prior work.PriorTurns, _ work.AgentPromptContext) (string, []byte, error) {
	renderer.key, renderer.detail, renderer.prior = key, detail, prior
	return renderer.prompt, renderer.schema, nil
}

func (*recordingPromptRenderer) Decode(work.Stage, []byte) (work.StageOutput, error) {
	return work.StageOutput{}, fmt.Errorf("Decode must not run while preparing")
}

type decodingPromptRenderer struct{}

func (decodingPromptRenderer) Render(work.StageKey, work.TicketDetail, work.PriorTurns, work.AgentPromptContext) (string, []byte, error) {
	return "", nil, fmt.Errorf("Render must not run while finalizing")
}

func (decodingPromptRenderer) Decode(stage work.Stage, result []byte) (work.StageOutput, error) {
	return prompts.Decode(stage, result)
}

func TestFinalizeDecodesEachStageResultFromItsTextReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		stage work.Stage
		text  string
		check func(*testing.T, work.StageOutput)
	}{
		{name: "plan", stage: work.StagePlan, text: `{"document":"the plan"}`, check: func(t *testing.T, output work.StageOutput) {
			t.Helper()
			value, ok := output.Value().(work.DocumentOutput)
			if !ok || value.Document != "the plan" {
				t.Fatalf("plan output = %#v", output.Value())
			}
		}},
		{name: "implement", stage: work.StageImplement, text: `{"report":"implemented","blocked":false,"blocked_reason":"","title":"Ship it","body":"Details"}`, check: func(t *testing.T, output work.StageOutput) {
			t.Helper()
			value, ok := output.Value().(work.ImplementOutput)
			if !ok || value.Report != "implemented" || value.Blocked || value.Title != "Ship it" || value.Body != "Details" {
				t.Fatalf("implement output = %#v", output.Value())
			}
		}},
		{name: "review", stage: work.StageReview, text: `{"document":"reviewed","findings":[{"id":"f1","blocking":true,"summary":"fix it"}],"verified":["tests"]}`, check: func(t *testing.T, output work.StageOutput) {
			t.Helper()
			value, ok := output.Value().(work.ReviewOutput)
			if !ok || value.Document != "reviewed" || len(value.Findings) != 1 || value.Findings[0].ID != "f1" || len(value.Verified) != 1 {
				t.Fatalf("review output = %#v", output.Value())
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blobStore := blobs.NewMemStore()
			artifacts := agent.NewArtifactStore(blobStore)
			textRef, err := artifacts.StoreText(t.Context(), "agent/run-7/"+string(test.stage)+"/1", test.text)
			if err != nil {
				t.Fatalf("StoreText() error = %v", err)
			}
			promptActivities, err := agentactivities.NewPromptActivities(decodingPromptRenderer{}, blobStore)
			if err != nil {
				t.Fatalf("NewPromptActivities() error = %v", err)
			}
			finalized, err := promptActivities.DecodeFinalOutput(t.Context(), agentactivities.FinalizeInput{Stage: test.stage, TextRef: textRef})
			if err != nil {
				t.Fatalf("Finalize() error = %v", err)
			}
			if finalized.Result == nil || finalized.Result.Stage() != test.stage {
				t.Fatalf("Finalize() result = %#v, want stage %q", finalized.Result, test.stage)
			}
			test.check(t, *finalized.Result)
		})
	}
}

func TestFinalizeRejectsAnIncompleteTextReferenceWithoutRetrying(t *testing.T) {
	t.Parallel()
	promptActivities, err := agentactivities.NewPromptActivities(decodingPromptRenderer{}, blobs.NewMemStore())
	if err != nil {
		t.Fatalf("NewPromptActivities() error = %v", err)
	}
	_, err = promptActivities.DecodeFinalOutput(t.Context(), agentactivities.FinalizeInput{Stage: work.StagePlan})
	var applicationError *temporal.ApplicationError
	if !errors.As(err, &applicationError) || applicationError.Type() != agent.ErrorTypeInvalidProviderOutcome ||
		!applicationError.NonRetryable() {
		t.Fatalf("Finalize() error = %T %v, want non-retryable %q", err, err, agent.ErrorTypeInvalidProviderOutcome)
	}
}

func TestPrepareRendersTheStageAndStoresReferenceBackedModelInput(t *testing.T) {
	t.Parallel()

	renderer := &recordingPromptRenderer{
		prompt: "implement the ticket using the available tools",
		schema: []byte(`{"type":"object","properties":{"report":{"type":"string"}},"required":["report"],"additionalProperties":false}`),
	}
	blobStore := blobs.NewMemStore()
	promptActivities, err := agentactivities.NewPromptActivities(renderer, blobStore)
	if err != nil {
		t.Fatalf("NewPromptActivities() error = %v", err)
	}
	attempt := activities.StageAttempt{
		Key:    work.StageKey{Ticket: 7, RunID: "run-7", Stage: work.StageImplement, Turn: 2},
		Model:  work.Model{Name: "gpt-test", Effort: "medium"},
		Detail: work.TicketDetail{Ticket: work.Ticket{Number: 7, Title: "Do the work", Body: "Please ship it"}},
	}
	prepared, err := promptActivities.Prepare(t.Context(), agentactivities.PrepareInput{Attempt: attempt, CacheKey: "run-7-implement"})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if renderer.key != attempt.Key || renderer.detail != attempt.Detail {
		t.Fatalf("Render input key=%#v detail=%#v", renderer.key, renderer.detail)
	}
	if prepared.PromptCacheKey != "run-7-implement" || prepared.ResponseFormat.Name != "implement_result" {
		t.Fatalf("Prepare() output = %#v", prepared)
	}
	items, err := agent.NewConversationStore(blobStore).Items(t.Context(), prepared.ConversationRef)
	if err != nil {
		t.Fatalf("Items() error = %v", err)
	}
	if len(items) != 2 || items[0].Kind != agent.ItemInstructions || items[1].Kind != agent.ItemUserText ||
		items[1].Text != renderer.prompt {
		t.Fatalf("prepared conversation items = %#v", items)
	}
	schema, err := agent.NewArtifactStore(blobStore).LoadResponseSchema(t.Context(), prepared.ResponseFormat.SchemaRef)
	if err != nil {
		t.Fatalf("LoadResponseSchema() error = %v", err)
	}
	if string(schema) != string(renderer.schema) {
		t.Fatalf("stored response schema = %s, want %s", schema, renderer.schema)
	}
	events, err := agent.NewTranscriptStore(blobStore).Events(t.Context(), prepared.TranscriptRef)
	if err != nil {
		t.Fatalf("Transcript Events() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != agent.EventWorkflowPrepared {
		t.Fatalf("prepared transcript = %#v", events)
	}
}

func TestPrepareSeedsAnImplementAttemptWithTheFullPriorConversationAndNewPrompt(t *testing.T) {
	t.Parallel()

	renderer := &recordingPromptRenderer{prompt: "Address the reviewer feedback.", schema: []byte(`{"type":"object"}`)}
	blobStore := blobs.NewMemStore()
	promptActivities, err := agentactivities.NewPromptActivities(renderer, blobStore)
	if err != nil {
		t.Fatalf("NewPromptActivities() error = %v", err)
	}
	source := activities.StageAttempt{Key: work.StageKey{Ticket: 7, RunID: "run-7", Stage: work.StageImplement, Turn: 1}}
	target := activities.StageAttempt{Key: work.StageKey{Ticket: 7, RunID: "run-7", Stage: work.StageImplement, Turn: 2}}
	sourceIdentity := "agent/run-7/step/8/attempt/1"
	targetIdentity := "agent/run-7/step/9/attempt/2"
	priorItems := []agent.ConversationItem{
		{Kind: agent.ItemInstructions, Text: "Follow the project rules."},
		{Kind: agent.ItemUserText, Text: "Implement the plan."},
		{Kind: agent.ItemAssistantText, Text: "I changed the service."},
		{Kind: agent.ItemFunctionCall, CallID: "call_1", Name: "go_test"},
		{Kind: agent.ItemFunctionOutput, CallID: "call_1", Output: "PASS"},
	}
	conversations := agent.NewConversationStore(blobStore)
	priorRef, err := conversations.Append(t.Context(), sourceIdentity, nil, priorItems)
	if err != nil {
		t.Fatalf("Append() prior conversation: %v", err)
	}

	prepared, err := promptActivities.Prepare(t.Context(), agentactivities.PrepareInput{
		Attempt: target, Identity: targetIdentity, CacheKey: "agent/run-7/implement/2",
		Seed: &agent.ConversationSeed{Source: source.Key, SourceIdentity: sourceIdentity, ConversationRef: priorRef},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if identity, err := conversations.Identity(prepared.ConversationRef); err != nil || identity != targetIdentity {
		t.Fatalf("seeded conversation identity = %q, %v", identity, err)
	}
	if prepared.ConversationRef.Key == priorRef.Key {
		t.Fatal("seeded attempt reused the prior attempt's conversation revision")
	}
	items, err := conversations.Items(t.Context(), prepared.ConversationRef)
	if err != nil {
		t.Fatalf("Items() error = %v", err)
	}
	want := append(append([]agent.ConversationItem{}, priorItems...), agent.ConversationItem{Kind: agent.ItemUserText, Text: renderer.prompt})
	if fmt.Sprintf("%#v", items) != fmt.Sprintf("%#v", want) {
		t.Fatalf("seeded conversation = %#v, want %#v", items, want)
	}
}

func TestPrepareRejectsInvalidOrCrossAttemptConversationSeeds(t *testing.T) {
	t.Parallel()

	renderer := &recordingPromptRenderer{prompt: "Address the reviewer feedback.", schema: []byte(`{"type":"object"}`)}
	blobStore := blobs.NewMemStore()
	promptActivities, err := agentactivities.NewPromptActivities(renderer, blobStore)
	if err != nil {
		t.Fatalf("NewPromptActivities() error = %v", err)
	}
	conversations := agent.NewConversationStore(blobStore)
	source := work.StageKey{Ticket: 7, RunID: "run-7", Stage: work.StageImplement, Turn: 1}
	target := activities.StageAttempt{Key: work.StageKey{Ticket: 7, RunID: "run-7", Stage: work.StageImplement, Turn: 2}}
	sourceIdentity := "agent/run-7/step/8/attempt/1"
	priorRef, err := conversations.Append(t.Context(), sourceIdentity, nil, []agent.ConversationItem{{Kind: agent.ItemUserText, Text: "prior"}})
	if err != nil {
		t.Fatalf("Append() prior conversation: %v", err)
	}
	otherRef, err := conversations.Append(t.Context(), "agent/run-other/step/8/attempt/1", nil, []agent.ConversationItem{{Kind: agent.ItemUserText, Text: "other"}})
	if err != nil {
		t.Fatalf("Append() other conversation: %v", err)
	}

	seeds := map[string]*agent.ConversationSeed{
		"same attempt":            {Source: target.Key, SourceIdentity: sourceIdentity, ConversationRef: priorRef},
		"review target":           {Source: source, SourceIdentity: sourceIdentity, ConversationRef: priorRef},
		"cross attempt reference": {Source: source, SourceIdentity: sourceIdentity, ConversationRef: otherRef},
		"corrupt reference":       {Source: source, SourceIdentity: sourceIdentity, ConversationRef: agent.ConversationRef{Key: priorRef.Key, Revision: priorRef.Revision, Bytes: priorRef.Bytes, Digest: "corrupt"}},
	}
	for name, seed := range seeds {
		t.Run(name, func(t *testing.T) {
			attempt := target
			if name == "review target" {
				attempt.Key.Stage = work.StageReview
			}
			_, err := promptActivities.Prepare(t.Context(), agentactivities.PrepareInput{Attempt: attempt, CacheKey: "cache", Seed: seed})
			var applicationError *temporal.ApplicationError
			if !errors.As(err, &applicationError) || !applicationError.NonRetryable() {
				t.Fatalf("Prepare() error = %v, want non-retryable invalid seed", err)
			}
		})
	}
}

var (
	_ agentactivities.PromptRenderer = decodingPromptRenderer{}
	_ agentactivities.PromptRenderer = (*recordingPromptRenderer)(nil)
)
