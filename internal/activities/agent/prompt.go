package agentactivities

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/work"
)

// PromptActivities owns stage prompt preparation and final result decoding.
type PromptActivities struct {
	prompts       PromptRenderer
	conversations agent.ConversationStore
	transcripts   agent.TranscriptStore
	artifacts     agent.ArtifactStore
}

const stageAgentInstructions = "Complete the supplied software-factory stage using the available tools. Return the final answer in the required structured format."

// NewPromptActivities constructs the prompt activities over the shared blob store.
func NewPromptActivities(renderer PromptRenderer, blobStore blobs.Store) (*PromptActivities, error) {
	if renderer == nil {
		return nil, fmt.Errorf("agent prompt activities need a prompt renderer")
	}
	if blobStore == nil {
		return nil, fmt.Errorf("agent prompt activities need a blob store")
	}
	return &PromptActivities{
		prompts: renderer, conversations: agent.NewConversationStore(blobStore),
		transcripts: agent.NewTranscriptStore(blobStore), artifacts: agent.NewArtifactStore(blobStore),
	}, nil
}

// Prepare renders one stage and persists its attempt-owned initial conversation
// plus response schema. A seeded implement copies immutable prior items into a
// new lineage before adding the new feedback prompt.
func (activities *PromptActivities) Prepare(ctx context.Context, input PrepareInput) (PrepareOutput, error) {
	prompt, schema, err := activities.prompts.Render(input.Attempt.Key, input.Attempt.Detail, input.Attempt.Prior, input.Attempt.PromptContext, input.Attempt.MaxReviewSteps)
	if err != nil {
		return PrepareOutput{}, invalidInput("render %s prompt: %v", input.Attempt.Key.Stage, err)
	}
	if strings.TrimSpace(prompt) == "" || !json.Valid(schema) {
		return PrepareOutput{}, invalidInput("render %s prompt returned blank prompt or invalid schema", input.Attempt.Key.Stage)
	}
	identity, err := agent.ConversationIdentity(input.Identity, input.Attempt.Key)
	if err != nil {
		return PrepareOutput{}, invalidInput("resolve target conversation identity: %v", err)
	}
	conversationRef, err := activities.prepareConversation(ctx, identity, input.Attempt.Key, input.Seed, prompt)
	if err != nil {
		return PrepareOutput{}, err
	}
	schemaRef, err := activities.artifacts.StoreResponseSchema(ctx, identity, schema)
	if err != nil {
		return PrepareOutput{}, transientFailure("store agent response schema", err)
	}
	transcriptRef, err := activities.transcripts.Append(ctx, identity, nil, agent.TranscriptEvent{Type: agent.EventWorkflowPrepared})
	if err != nil {
		return PrepareOutput{}, transientFailure("start agent transcript", err)
	}
	return PrepareOutput{
		ConversationRef: conversationRef,
		TranscriptRef:   transcriptRef,
		ResponseFormat: agent.ResponseFormatRef{
			Name: string(input.Attempt.Key.Stage) + "_result", SchemaRef: schemaRef,
		},
		PromptCacheKey: input.CacheKey,
	}, nil
}

func (activities *PromptActivities) prepareConversation(
	ctx context.Context,
	identity string,
	target work.StageKey,
	seed *agent.ConversationSeed,
	prompt string,
) (agent.ConversationRef, error) {
	if seed == nil {
		conversationRef, err := activities.conversations.Append(ctx, identity, nil, []agent.ConversationItem{
			{Kind: agent.ItemInstructions, Text: stageAgentInstructions},
			{Kind: agent.ItemUserText, Text: prompt},
		})
		if err != nil {
			return agent.ConversationRef{}, transientFailure("store initial agent conversation", err)
		}
		return conversationRef, nil
	}
	if err := seed.ValidateFor(target); err != nil {
		return agent.ConversationRef{}, invalidInput("validate conversation seed: %v", err)
	}
	actualIdentity, err := activities.conversations.Identity(seed.ConversationRef)
	if err != nil {
		return agent.ConversationRef{}, invalidInput("resolve conversation seed identity: %v", err)
	}
	if actualIdentity != seed.SourceIdentity {
		return agent.ConversationRef{}, invalidInput("conversation seed belongs to %q, not %q", actualIdentity, seed.SourceIdentity)
	}
	items, err := activities.conversations.Items(ctx, seed.ConversationRef)
	if err != nil {
		return agent.ConversationRef{}, invalidInput("load conversation seed: %v", err)
	}
	conversationRef, err := activities.conversations.Append(ctx, identity, nil, items)
	if err != nil {
		return agent.ConversationRef{}, transientFailure("copy seeded agent conversation", err)
	}
	conversationRef, err = activities.conversations.Append(ctx, identity, &conversationRef, []agent.ConversationItem{{
		Kind: agent.ItemUserText, Text: prompt,
	}})
	if err != nil {
		return agent.ConversationRef{}, transientFailure("append seeded agent prompt", err)
	}
	return conversationRef, nil
}

// DecodeFinalOutput loads terminal model text and decodes the stage's existing result envelope.
func (activities *PromptActivities) DecodeFinalOutput(ctx context.Context, input FinalizeInput) (FinalizeOutput, error) {
	if input.TextRef.Key == "" || input.TextRef.Bytes < 1 || input.TextRef.Digest == "" {
		return FinalizeOutput{}, invalidProviderOutcome("final model text reference is incomplete")
	}
	text, err := activities.artifacts.LoadText(ctx, input.TextRef)
	if err != nil {
		return FinalizeOutput{}, transientFailure("load final agent text", err)
	}
	result, err := activities.prompts.Decode(input.Stage, []byte(text))
	if err != nil {
		return FinalizeOutput{}, invalidProviderOutcome("decode final %s output: %v", input.Stage, err)
	}
	output := FinalizeOutput{Result: &result, TranscriptRef: input.TranscriptRef}
	if input.TranscriptRef.Key != "" {
		identity, err := activities.transcripts.Identity(input.TranscriptRef)
		if err != nil {
			return FinalizeOutput{}, invalidInput("resolve final transcript identity: %v", err)
		}
		output.TranscriptRef, err = activities.transcripts.Append(
			ctx, identity, &input.TranscriptRef, agent.TranscriptEvent{Type: agent.EventFinalOutputDecoded},
		)
		if err != nil {
			return FinalizeOutput{}, transientFailure("append final transcript event", err)
		}
	}
	return output, nil
}
