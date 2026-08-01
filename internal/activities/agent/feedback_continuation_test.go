package agentactivities_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/clients/codexresponses"
	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/work"
)

func TestFeedbackContinuationReachesTheProviderWithTheSuccessfulImplementConversation(t *testing.T) {
	t.Parallel()

	blobStore := blobs.NewMemStore()
	conversations := agent.NewConversationStore(blobStore)
	source := activities.StageAttempt{
		Key: work.StageKey{Ticket: 7, RunID: "run-7", Stage: work.StageImplement, Turn: 1},
	}
	target := activities.StageAttempt{
		Key:   work.StageKey{Ticket: 7, RunID: "run-7", Stage: work.StageImplement, Turn: 2},
		Model: work.Model{Name: "gpt-test", Effort: "medium"},
	}
	sourceIdentity := "agent/run-7/step/8/attempt/1"
	priorRef, err := conversations.Append(t.Context(), sourceIdentity, nil, []agent.ConversationItem{
		{Kind: agent.ItemInstructions, Text: "Complete the implementation."},
		{Kind: agent.ItemUserText, Text: "Implement the accepted plan."},
		{Kind: agent.ItemAssistantText, Text: "Implemented the plan and opened the pull request."},
	})
	if err != nil {
		t.Fatalf("Append() successful implement conversation: %v", err)
	}
	promptActivities, err := agentactivities.NewPromptActivities(&recordingPromptRenderer{
		prompt: "Fix the failing test on candidate abc123.",
		schema: []byte(`{"type":"object"}`),
	}, blobStore)
	if err != nil {
		t.Fatalf("NewPromptActivities() error = %v", err)
	}
	prepared, err := promptActivities.Prepare(t.Context(), agentactivities.PrepareInput{
		Attempt:  target,
		Identity: "agent/run-7/step/9/attempt/1",
		CacheKey: "agent/run-7/implement/2",
		Seed: &agent.ConversationSeed{
			Source: source.Key, SourceIdentity: sourceIdentity, ConversationRef: priorRef,
		},
	})
	if err != nil {
		t.Fatalf("Prepare() feedback continuation: %v", err)
	}

	turner := &fakeTurner{result: codexresponses.TurnResult{
		Outcome: codexresponses.OutcomeFinalText,
		Text:    `{"report":"fixed"}`,
	}}
	modelActivities, err := agentactivities.NewActivities(
		turner,
		blobStore,
		clocktest.NewFake(time.Unix(0, 0)),
		agenttool.MustSet("coding-write-v1"),
	)
	if err != nil {
		t.Fatalf("NewActivities() error = %v", err)
	}
	_, err = modelActivities.ModelTurn(t.Context(), agent.ModelTurnInput{
		Model:              target.Model,
		ToolsetID:          "coding-write-v1",
		ConversationRef:    prepared.ConversationRef,
		TranscriptRef:      prepared.TranscriptRef,
		ResponseFormat:     prepared.ResponseFormat,
		PromptCacheKey:     prepared.PromptCacheKey,
		ModelTurn:          1,
		IdempotencyKey:     "agent/run-7/step/9/attempt/1/model/1",
		ToolsetFingerprint: agenttool.MustSet("coding-write-v1").Fingerprint(),
	})
	if err != nil {
		t.Fatalf("ModelTurn() feedback continuation: %v", err)
	}

	if turner.request.Instructions != "Complete the implementation." {
		t.Fatalf("provider instructions = %q, want seeded implement instructions", turner.request.Instructions)
	}
	want := []codexresponses.InputItem{
		codexresponses.UserText("Implement the accepted plan."),
		codexresponses.AssistantText("Implemented the plan and opened the pull request."),
		codexresponses.UserText("Fix the failing test on candidate abc123."),
	}
	if len(turner.request.Input) != len(want) {
		t.Fatalf("provider input = %#v, want %#v", turner.request.Input, want)
	}
	for index := range want {
		if !reflect.DeepEqual(turner.request.Input[index], want[index]) {
			t.Fatalf("provider input[%d] = %#v, want %#v", index, turner.request.Input[index], want[index])
		}
	}
	if turner.request.Store || turner.request.PreviousResponseID != "" {
		t.Fatalf("provider continuation relies on provider state: %#v", turner.request)
	}
}
