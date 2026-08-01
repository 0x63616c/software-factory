// Package agentactivities contains production agent side-effect boundaries.
package agentactivities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/clients/codexresponses"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Turner is the sealed direct model boundary used by a model-turn activity.
type Turner interface {
	Turn(context.Context, codexresponses.TurnRequest, codexresponses.EmitFunc) (codexresponses.TurnResult, error)
}

// Activities owns the production model-side agent activities.
type Activities struct {
	turner        Turner
	conversations agent.ConversationStore
	transcripts   agent.TranscriptStore
	artifacts     agent.ArtifactStore
	clock         clock.Clock
	metrics       AgentMetrics
	logger        *slog.Logger
	toolsets      map[agent.ToolsetID]agenttool.Set
}

// StreamProgress is content-free provider stream heartbeat metadata.
type StreamProgress struct {
	EventType codexresponses.EventType
	Events    int
}

// RecordLifecycle records one terminal child outcome without carrying prompt,
// conversation, result, error, or credential content across the boundary.
func (activities *Activities) RecordLifecycle(_ context.Context, input LifecycleInput) error {
	switch input.Outcome {
	case telemetry.AgentOutcomeSucceeded, telemetry.AgentOutcomeFailed, telemetry.AgentOutcomeCancelled:
	case telemetry.AgentOutcomeFinalText, telemetry.AgentOutcomeToolCalls, telemetry.AgentOutcomeToolError:
		return invalidInput("unsupported agent child outcome %q", input.Outcome)
	default:
		return invalidInput("unsupported agent child outcome %q", input.Outcome)
	}
	activities.metrics.AgentChildFinished(input.Outcome)
	return nil
}

// NewActivities constructs model-side activities for isolated acceptance harnesses.
// Production composition uses NewObservedActivities so metrics and structured
// decision logs cannot be accidentally detached from the served registry.
func NewActivities(turner Turner, blobStore blobs.Store, clk clock.Clock, toolsets ...agenttool.Set) (*Activities, error) {
	return newActivities(turner, blobStore, clk, noopAgentMetrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)), toolsets...)
}

// NewObservedActivities constructs production model-side agent activities.
func NewObservedActivities(
	turner Turner,
	blobStore blobs.Store,
	clk clock.Clock,
	metrics AgentMetrics,
	logger *slog.Logger,
	toolsets ...agenttool.Set,
) (*Activities, error) {
	return newActivities(turner, blobStore, clk, metrics, logger, toolsets...)
}

func newActivities(
	turner Turner,
	blobStore blobs.Store,
	clk clock.Clock,
	metrics AgentMetrics,
	logger *slog.Logger,
	toolsets ...agenttool.Set,
) (*Activities, error) {
	if turner == nil {
		return nil, fmt.Errorf("agent activities need a model turner")
	}
	if blobStore == nil {
		return nil, fmt.Errorf("agent activities need a blob store")
	}
	if clk == nil {
		return nil, fmt.Errorf("agent activities need a clock")
	}
	if metrics == nil {
		return nil, fmt.Errorf("agent activities need metrics")
	}
	if logger == nil {
		return nil, fmt.Errorf("agent activities need a logger")
	}
	byID := make(map[agent.ToolsetID]agenttool.Set, len(toolsets))
	for _, toolset := range toolsets {
		if _, exists := byID[toolset.ID()]; exists {
			return nil, fmt.Errorf("agent activities received duplicate toolset %q", toolset.ID())
		}
		byID[toolset.ID()] = toolset
	}
	return &Activities{
		turner:        turner,
		conversations: agent.NewConversationStore(blobStore),
		transcripts:   agent.NewTranscriptStore(blobStore),
		artifacts:     agent.NewArtifactStore(blobStore),
		clock:         clk,
		metrics:       metrics,
		logger:        logger,
		toolsets:      byID,
	}, nil
}

// ModelTurn loads one conversation revision, calls the provider, and stores its durable result.
func (activities *Activities) ModelTurn(ctx context.Context, input agent.ModelTurnInput) (agent.ModelTurnResult, error) {
	toolset, ok := activities.toolsets[input.ToolsetID]
	if !ok {
		return agent.ModelTurnResult{}, invalidInput("unknown agent toolset %q", input.ToolsetID)
	}
	fingerprint := toolset.Fingerprint()
	if input.ToolsetFingerprint != "" && input.ToolsetFingerprint != fingerprint {
		return agent.ModelTurnResult{}, invalidInput(
			"agent toolset %q fingerprint changed: got %q, want %q",
			input.ToolsetID, input.ToolsetFingerprint, fingerprint,
		)
	}
	items, err := activities.conversations.Items(ctx, input.ConversationRef)
	if err != nil {
		return agent.ModelTurnResult{}, transientFailure("load model conversation", err)
	}
	responseFormat, err := activities.responseFormat(ctx, input.ResponseFormat)
	if err != nil {
		return agent.ModelTurnResult{}, fmt.Errorf("resolve agent response format: %w", err)
	}
	request, err := modelRequest(input, toolset, items, responseFormat)
	if err != nil {
		return agent.ModelTurnResult{}, invalidInput("build model request: %v", err)
	}
	identity, err := activities.conversations.Identity(input.ConversationRef)
	if err != nil {
		return agent.ModelTurnResult{}, invalidInput("resolve model conversation identity: %v", err)
	}
	attempt := activityAttempt(ctx)
	if attempt > 1 {
		activities.metrics.AgentActivityRetry(agent.ModelTurnActivityName)
	}
	activities.logger.InfoContext(ctx, "agent model turn started",
		slog.String("agent", identity),
		slog.Int("model_turn", input.ModelTurn),
		slog.Int("activity_attempt", int(attempt)),
		slog.String("model", input.Model.Name),
		slog.String("effort", input.Model.Effort),
	)
	events := 0
	started := activities.clock.Now()
	providerResult, err := activities.turner.Turn(ctx, request, func(event codexresponses.Event) {
		events++
		activity.RecordHeartbeat(ctx, StreamProgress{EventType: event.Type, Events: events})
	})
	if err != nil {
		outcome := telemetry.AgentOutcomeFailed
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = telemetry.AgentOutcomeCancelled
		}
		took := activities.clock.Now().Sub(started)
		activities.metrics.AgentModelTurn(input.Model, outcome, work.Usage{}, false, input.ConversationRef.Bytes, took)
		activities.logger.WarnContext(ctx, "agent model turn failed",
			slog.String("agent", identity),
			slog.Int("model_turn", input.ModelTurn),
			slog.Int("activity_attempt", int(attempt)),
			slog.String("outcome", string(outcome)),
			slog.Int64("duration_millis", took.Milliseconds()),
		)
		return agent.ModelTurnResult{}, providerFailure(ctx, err)
	}
	var result agent.ModelTurnResult
	switch providerResult.Outcome {
	case codexresponses.OutcomeFinalText:
		result, err = activities.storeFinalTurn(ctx, input.ConversationRef, identity, providerResult)
	case codexresponses.OutcomeToolCalls:
		result, err = activities.storeToolTurn(ctx, input.ConversationRef, identity, providerResult)
	default:
		return agent.ModelTurnResult{}, invalidProviderOutcome("model turn has unknown outcome %q", providerResult.Outcome)
	}
	if err != nil {
		return agent.ModelTurnResult{}, fmt.Errorf("store agent model outcome: %w", err)
	}
	result.ToolsetFingerprint = fingerprint
	outcome := telemetry.AgentOutcomeFinalText
	if result.Outcome == agent.OutcomeToolCalls {
		outcome = telemetry.AgentOutcomeToolCalls
	}
	took := activities.clock.Now().Sub(started)
	activities.metrics.AgentModelTurn(input.Model, outcome, result.Usage, result.UsageMeasured, result.ConversationRef.Bytes, took)
	activities.logger.InfoContext(ctx, "agent model turn completed",
		slog.String("agent", identity),
		slog.Int("model_turn", input.ModelTurn),
		slog.Int("activity_attempt", int(attempt)),
		slog.String("outcome", string(outcome)),
		slog.Int("tool_calls", len(result.ToolCalls)),
		slog.Int64("conversation_bytes", result.ConversationRef.Bytes),
		slog.Int64("duration_millis", took.Milliseconds()),
	)
	if input.TranscriptRef.Key != "" {
		result.TranscriptRef, err = activities.transcripts.Append(ctx, identity, &input.TranscriptRef, agent.TranscriptEvent{
			Type: agent.EventModelCompleted, ModelTurn: input.ModelTurn, Outcome: string(result.Outcome),
			Usage: result.Usage, UsageMeasured: result.UsageMeasured,
			DurationMillis: took.Milliseconds(),
		})
		if err != nil {
			return agent.ModelTurnResult{}, transientFailure("append model transcript event", err)
		}
	}
	return result, nil
}

func (activities *Activities) responseFormat(
	ctx context.Context,
	ref agent.ResponseFormatRef,
) (*codexresponses.ResponseFormat, error) {
	if ref.Name == "" && ref.SchemaRef.Key == "" {
		return nil, nil
	}
	if ref.Name == "" || ref.SchemaRef.Key == "" {
		return nil, invalidInput("agent response format is incomplete")
	}
	schema, err := activities.artifacts.LoadResponseSchema(ctx, ref.SchemaRef)
	if err != nil {
		return nil, transientFailure("load agent response schema", err)
	}
	if !json.Valid(schema) {
		return nil, invalidInput("agent response schema is not valid JSON")
	}
	return &codexresponses.ResponseFormat{Name: ref.Name, Schema: schema}, nil
}

func (activities *Activities) storeFinalTurn(
	ctx context.Context,
	predecessor agent.ConversationRef,
	identity string,
	providerResult codexresponses.TurnResult,
) (agent.ModelTurnResult, error) {
	if strings.TrimSpace(providerResult.Text) == "" {
		return agent.ModelTurnResult{}, invalidProviderOutcome("final-text outcome contains blank text")
	}
	conversationRef, err := activities.conversations.Append(ctx, identity, &predecessor, []agent.ConversationItem{{
		Kind: agent.ItemAssistantText,
		Text: providerResult.Text,
	}})
	if err != nil {
		return agent.ModelTurnResult{}, transientFailure("store final model conversation", err)
	}
	textRef, err := activities.artifacts.StoreText(ctx, identity, providerResult.Text)
	if err != nil {
		return agent.ModelTurnResult{}, transientFailure("store final model text", err)
	}
	return agent.ModelTurnResult{
		Outcome:         agent.OutcomeFinalText,
		ConversationRef: conversationRef,
		FinalTextRef:    textRef,
		Usage: work.Usage{
			InputTokens:  providerResult.Usage.InputTokens,
			OutputTokens: providerResult.Usage.OutputTokens,
		},
		UsageMeasured: true,
	}, nil
}

func (activities *Activities) storeToolTurn(
	ctx context.Context,
	predecessor agent.ConversationRef,
	identity string,
	providerResult codexresponses.TurnResult,
) (agent.ModelTurnResult, error) {
	if len(providerResult.ToolCalls) == 0 {
		return agent.ModelTurnResult{}, invalidProviderOutcome("tool-call outcome contains no tool calls")
	}
	for index, call := range providerResult.ToolCalls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.CallID) == "" ||
			strings.TrimSpace(call.Name) == "" || !json.Valid(call.Arguments) {
			return agent.ModelTurnResult{}, invalidProviderOutcome("tool call %d is incomplete", index)
		}
	}
	pending := make([]agent.PendingToolCall, 0, len(providerResult.ToolCalls))
	items := make([]agent.ConversationItem, 0, len(providerResult.ToolCalls))
	for _, call := range providerResult.ToolCalls {
		argumentsRef, err := activities.artifacts.StoreArguments(ctx, identity, call.Arguments)
		if err != nil {
			return agent.ModelTurnResult{}, transientFailure(
				fmt.Sprintf("store arguments for tool call %q", call.CallID),
				err,
			)
		}
		pending = append(pending, agent.PendingToolCall{
			CallID:       call.CallID,
			Name:         call.Name,
			ArgumentsRef: argumentsRef,
		})
		items = append(items, agent.ConversationItem{
			Kind:      agent.ItemFunctionCall,
			ID:        call.ID,
			CallID:    call.CallID,
			Name:      call.Name,
			Arguments: append([]byte(nil), call.Arguments...),
		})
	}
	conversationRef, err := activities.conversations.Append(ctx, identity, &predecessor, items)
	if err != nil {
		return agent.ModelTurnResult{}, transientFailure("store tool-call model conversation", err)
	}
	return agent.ModelTurnResult{
		Outcome:         agent.OutcomeToolCalls,
		ConversationRef: conversationRef,
		ToolCalls:       pending,
		Usage: work.Usage{
			InputTokens:  providerResult.Usage.InputTokens,
			OutputTokens: providerResult.Usage.OutputTokens,
		},
		UsageMeasured: true,
	}, nil
}

func invalidProviderOutcome(format string, args ...any) error {
	return temporal.NewNonRetryableApplicationError(
		fmt.Sprintf(format, args...),
		agent.ErrorTypeInvalidProviderOutcome,
		nil,
	)
}

func invalidInput(format string, args ...any) error {
	return temporal.NewNonRetryableApplicationError(
		fmt.Sprintf(format, args...),
		agent.ErrorTypeInvalidInput,
		nil,
	)
}

func providerFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("run direct model turn: %w", err)
	}
	if errors.Is(err, codexresponses.ErrRateLimited) {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("run direct model turn: %v", err),
			agent.ErrorTypeRateLimit,
			err,
		)
	}
	if errors.Is(err, codexresponses.ErrAuth) || errors.Is(err, work.ErrPermanent) {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("run direct model turn: %v", err),
			agent.ErrorTypeAuth,
			err,
		)
	}
	return transientFailure("run direct model turn", err)
}

func transientFailure(operation string, err error) error {
	return temporal.NewApplicationErrorWithOptions(
		fmt.Sprintf("%s: %v", operation, err),
		agent.ErrorTypeTransient,
		temporal.ApplicationErrorOptions{Cause: err},
	)
}

func activityAttempt(ctx context.Context) int32 {
	if !activity.IsActivity(ctx) {
		return 1
	}
	return activity.GetInfo(ctx).Attempt
}

func modelRequest(
	input agent.ModelTurnInput,
	toolset agenttool.Set,
	items []agent.ConversationItem,
	responseFormat *codexresponses.ResponseFormat,
) (codexresponses.TurnRequest, error) {
	if err := input.Model.Validate(); err != nil {
		return codexresponses.TurnRequest{}, fmt.Errorf("validate model turn: %w", err)
	}
	var instructions string
	providerItems := make([]codexresponses.InputItem, 0, len(items))
	for _, item := range items {
		switch item.Kind {
		case agent.ItemInstructions:
			if instructions != "" {
				return codexresponses.TurnRequest{}, fmt.Errorf("model conversation has multiple instruction blocks")
			}
			instructions = item.Text
		case agent.ItemUserText:
			providerItems = append(providerItems, codexresponses.UserText(item.Text))
		case agent.ItemAssistantText:
			providerItems = append(providerItems, codexresponses.AssistantText(item.Text))
		case agent.ItemFunctionCall:
			providerItems = append(providerItems, codexresponses.FunctionCall(codexresponses.ToolCall{
				ID:        item.ID,
				CallID:    item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			}))
		case agent.ItemFunctionOutput:
			providerItems = append(providerItems, codexresponses.FunctionOutput(item.CallID, item.Output))
		default:
			return codexresponses.TurnRequest{}, fmt.Errorf("model conversation has unsupported item kind %q", item.Kind)
		}
	}
	specifications := toolset.Specifications()
	tools := make([]codexresponses.Tool, 0, len(specifications))
	for _, specification := range specifications {
		tools = append(tools, codexresponses.Tool{
			Name:        specification.Name,
			Description: specification.Description,
			Parameters:  specification.Parameters,
		})
	}
	return codexresponses.TurnRequest{
		Model:             input.Model.Name,
		Instructions:      instructions,
		Input:             providerItems,
		Store:             false,
		Tools:             tools,
		ToolChoice:        codexresponses.ToolChoiceAuto,
		ParallelToolCalls: false,
		Reasoning: codexresponses.ReasoningOptions{
			Effort:  codexresponses.ReasoningEffort(input.Model.Effort),
			Summary: codexresponses.ReasoningSummaryAuto,
		},
		TextVerbosity:  codexresponses.TextVerbosityMedium,
		ResponseFormat: responseFormat,
		PromptCacheKey: input.PromptCacheKey,
		IdempotencyKey: input.IdempotencyKey,
		Include:        []string{"reasoning.encrypted_content"},
	}, nil
}
