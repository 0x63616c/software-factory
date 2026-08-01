// Package workflows contains the factory's deterministic Temporal orchestration.
package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	agentUnboundedChangeID       = "agent-workflow-unbounded-v1"
	agentUnboundedVersion        = 1
	agentContinueAsNewAfterTurns = 8
)

// legacyAgentLimits decodes and reproduces pre-unbounded workflow commands
// until their histories leave retention. New executions leave it zero.
type legacyAgentLimits struct {
	MaxModelTurns        int   `json:"max_model_turns"`
	MaxToolCalls         int   `json:"max_tool_calls"`
	MaxInputTokens       int64 `json:"max_input_tokens"`
	MaxOutputTokens      int64 `json:"max_output_tokens"`
	MaxConversationBytes int64 `json:"max_conversation_bytes"`
	ContinueAsNewAfter   int   `json:"continue_as_new_after"`
}

func defaultLegacyAgentLimits() *legacyAgentLimits {
	return &legacyAgentLimits{
		MaxModelTurns: 24, MaxToolCalls: 96, MaxInputTokens: 500_000, MaxOutputTokens: 100_000,
		MaxConversationBytes: 1 << 20, ContinueAsNewAfter: 8,
	}
}

// AgentWorkflowInput starts one stage agent.
type AgentWorkflowInput struct {
	Attempt         activities.StageAttempt
	ToolsetID       agent.ToolsetID
	ToolTarget      agent.ToolTarget
	LegacyLimits    *legacyAgentLimits `json:"limits,omitempty"`
	ModelTurnPolicy work.AgentActivityPolicy
	ControlPolicy   work.ActivityPolicy
	// Identity pins every durable agent artifact to one semantic execution.
	// Empty derives the identity from the stage key.
	Identity string
	CacheKey string
	Seed     *agent.ConversationSeed
	State    *AgentWorkflowState
}

// AgentWorkflowState is the reference-only state carried across Continue-As-New.
type AgentWorkflowState struct {
	ConversationRef    agent.ConversationRef
	TranscriptRef      agent.TranscriptRef
	ResponseFormat     agent.ResponseFormatRef
	ToolsetFingerprint string
	PromptCacheKey     string
	Usage              work.Usage
	UsageMeasured      bool
	ModelTurns         int
	ToolCalls          int
}

// AgentWorkflowResult is the typed result returned to WorkOnTicket.
type AgentWorkflowResult struct {
	Result          work.StageOutput
	Usage           work.Usage
	UsageMeasured   bool
	ConversationRef agent.ConversationRef
	TranscriptRef   agent.TranscriptRef
	Failure         *agent.TerminalFailure
	ModelTurns      int
	ToolCalls       int
}

// MarshalJSON requires exactly one terminal outcome. A StageOutput deliberately
// refuses to marshal its invalid zero value, so a malformed child result can
// never arrive at its parent looking successful.
func (result AgentWorkflowResult) MarshalJSON() ([]byte, error) {
	type wireResult struct {
		Result          *work.StageOutput      `json:"result,omitempty"`
		Usage           work.Usage             `json:"usage"`
		UsageMeasured   bool                   `json:"usage_measured"`
		ConversationRef agent.ConversationRef  `json:"conversation_ref"`
		TranscriptRef   agent.TranscriptRef    `json:"transcript_ref"`
		Failure         *agent.TerminalFailure `json:"failure,omitempty"`
		ModelTurns      int                    `json:"model_turns"`
		ToolCalls       int                    `json:"tool_calls"`
	}
	hasResult := result.Result.Stage() != ""
	if err := validateAgentWorkflowResult(hasResult, result.Failure); err != nil {
		return nil, err
	}
	encoded := wireResult{
		Usage:           result.Usage,
		UsageMeasured:   result.UsageMeasured,
		ConversationRef: result.ConversationRef,
		TranscriptRef:   result.TranscriptRef,
		Failure:         result.Failure,
		ModelTurns:      result.ModelTurns,
		ToolCalls:       result.ToolCalls,
	}
	if hasResult {
		encoded.Result = &result.Result
	}
	return json.Marshal(encoded)
}

// UnmarshalJSON restores exactly one successful stage result or terminal failure.
func (result *AgentWorkflowResult) UnmarshalJSON(data []byte) error {
	type wireResult struct {
		Result          *work.StageOutput      `json:"result"`
		Usage           work.Usage             `json:"usage"`
		UsageMeasured   bool                   `json:"usage_measured"`
		ConversationRef agent.ConversationRef  `json:"conversation_ref"`
		TranscriptRef   agent.TranscriptRef    `json:"transcript_ref"`
		Failure         *agent.TerminalFailure `json:"failure"`
		ModelTurns      int                    `json:"model_turns"`
		ToolCalls       int                    `json:"tool_calls"`
	}
	var decoded wireResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode agent workflow result: %w", err)
	}
	if err := validateAgentWorkflowResult(decoded.Result != nil, decoded.Failure); err != nil {
		return fmt.Errorf("decode agent workflow result: %w", err)
	}
	*result = AgentWorkflowResult{
		Usage:           decoded.Usage,
		UsageMeasured:   decoded.UsageMeasured,
		ConversationRef: decoded.ConversationRef,
		TranscriptRef:   decoded.TranscriptRef,
		Failure:         decoded.Failure,
		ModelTurns:      decoded.ModelTurns,
		ToolCalls:       decoded.ToolCalls,
	}
	if decoded.Result != nil {
		result.Result = *decoded.Result
	}
	return nil
}

func validateAgentWorkflowResult(hasResult bool, failure *agent.TerminalFailure) error {
	if hasResult == (failure != nil) {
		return fmt.Errorf("agent workflow result must contain exactly one of result or failure")
	}
	if failure != nil {
		if err := failure.Validate(); err != nil {
			return fmt.Errorf("validate agent workflow failure: %w", err)
		}
	}
	return nil
}

// AgentWorkflow runs one reference-only model/tool loop until it produces a
// result, an unrecoverable failure, or a time-based workflow deadline fires.
func AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (workflowResult AgentWorkflowResult, workflowErr error) {
	defer func() { recordAgentLifecycle(ctx, workflowErr, workflowResult.Failure) }()
	if err := validateAgentInput(input); err != nil {
		return AgentWorkflowResult{}, temporal.NewNonRetryableApplicationError(err.Error(), agent.ErrorTypeInvalidInput, err)
	}
	toolTaskQueue, err := input.ToolTarget.TaskQueue(input.Attempt.Key.RunID)
	if err != nil {
		return AgentWorkflowResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("resolve agent tool target: %v", err), agent.ErrorTypeInvalidInput, err,
		)
	}
	identity, err := agent.ConversationIdentity(input.Identity, input.Attempt.Key)
	if err != nil {
		return AgentWorkflowResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("resolve agent conversation identity: %v", err), agent.ErrorTypeInvalidInput, err,
		)
	}
	controlContext := workflow.WithActivityOptions(ctx, agentControlActivityOptions(input.ControlPolicy))
	modelContext := workflow.WithActivityOptions(ctx, agentModelTurnActivityOptions(input.ModelTurnPolicy))
	state := AgentWorkflowState{}
	unbounded := workflow.GetVersion(ctx, agentUnboundedChangeID, workflow.DefaultVersion, agentUnboundedVersion) != workflow.DefaultVersion
	if input.State == nil {
		attempt := input.Attempt
		if unbounded {
			attempt.LegacyMaxReviewSteps = 0
		}
		var prepared agentactivities.PrepareOutput
		if err := workflow.ExecuteActivity(controlContext, agent.PrepareActivityName, agentactivities.PrepareInput{
			Attempt: attempt, Identity: identity, CacheKey: input.CacheKey, Seed: input.Seed,
		}).Get(ctx, &prepared); err != nil {
			return AgentWorkflowResult{}, fmt.Errorf("prepare agent workflow: %w", err)
		}
		state.ConversationRef = prepared.ConversationRef
		state.TranscriptRef = prepared.TranscriptRef
		state.ResponseFormat = prepared.ResponseFormat
		state.PromptCacheKey = prepared.PromptCacheKey
		state.UsageMeasured = true
	} else {
		state = *input.State
	}
	result := AgentWorkflowResult{
		Usage: state.Usage, UsageMeasured: state.UsageMeasured, ConversationRef: state.ConversationRef, TranscriptRef: state.TranscriptRef,
		ModelTurns: state.ModelTurns, ToolCalls: state.ToolCalls,
	}
	conversationRef := state.ConversationRef
	if !unbounded && conversationRef.Bytes > input.LegacyLimits.MaxConversationBytes {
		return terminalLegacyAgentBudgetFailure(result, "conversation_bytes")
	}
	var sessionContext workflow.Context
	for {
		if !unbounded && result.ModelTurns >= input.LegacyLimits.MaxModelTurns {
			return terminalLegacyAgentBudgetFailure(result, "model_turns")
		}
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			state.ConversationRef = conversationRef
			state.TranscriptRef = result.TranscriptRef
			state.Usage = result.Usage
			state.UsageMeasured = result.UsageMeasured
			state.ModelTurns = result.ModelTurns
			state.ToolCalls = result.ToolCalls
			return result, continueAgentWorkflowAsNew(ctx, input, state, unbounded)
		}
		modelTurn := result.ModelTurns + 1
		var turn agent.ModelTurnResult
		if err := workflow.ExecuteActivity(modelContext, agent.ModelTurnActivityName, agent.ModelTurnInput{
			Model: input.Attempt.Model, ToolsetID: input.ToolsetID, ToolsetFingerprint: state.ToolsetFingerprint,
			ConversationRef: conversationRef, TranscriptRef: result.TranscriptRef,
			ResponseFormat: state.ResponseFormat, PromptCacheKey: state.PromptCacheKey, ModelTurn: modelTurn,
			IdempotencyKey: fmt.Sprintf("%s/model/%d", identity, modelTurn),
		}).Get(ctx, &turn); err != nil {
			if failure := modelTerminalFailure(err); failure != nil {
				return terminalAgentFailure(result, failure.Kind)
			}
			return result, fmt.Errorf("run agent model turn %d: %w", modelTurn, err)
		}
		result.ModelTurns++
		if turn.ToolsetFingerprint == "" {
			return terminalAgentFailure(result, agent.TerminalFailureInvalidProviderOutcome)
		}
		if state.ToolsetFingerprint == "" {
			state.ToolsetFingerprint = turn.ToolsetFingerprint
		} else if state.ToolsetFingerprint != turn.ToolsetFingerprint {
			return terminalAgentFailure(result, agent.TerminalFailureInvalidProviderOutcome)
		}
		result.Usage = result.Usage.Add(turn.Usage)
		result.UsageMeasured = result.UsageMeasured && turn.UsageMeasured
		conversationRef = turn.ConversationRef
		result.ConversationRef = conversationRef
		if turn.TranscriptRef.Key != "" {
			result.TranscriptRef = turn.TranscriptRef
		}
		if !unbounded && result.UsageMeasured && result.Usage.InputTokens > input.LegacyLimits.MaxInputTokens {
			return terminalLegacyAgentBudgetFailure(result, "input_tokens")
		}
		if !unbounded && result.UsageMeasured && result.Usage.OutputTokens > input.LegacyLimits.MaxOutputTokens {
			return terminalLegacyAgentBudgetFailure(result, "output_tokens")
		}
		if !unbounded && conversationRef.Bytes > input.LegacyLimits.MaxConversationBytes {
			return terminalLegacyAgentBudgetFailure(result, "conversation_bytes")
		}
		switch turn.Outcome {
		case agent.OutcomeToolCalls:
			if len(turn.ToolCalls) == 0 {
				return terminalAgentFailure(result, agent.TerminalFailureInvalidProviderOutcome)
			}
			if !unbounded && result.ToolCalls+len(turn.ToolCalls) > input.LegacyLimits.MaxToolCalls {
				return terminalLegacyAgentBudgetFailure(result, "tool_calls")
			}
			if sessionContext == nil {
				targetQueue := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
					TaskQueue: toolTaskQueue,
				})
				created, err := workflow.CreateSession(targetQueue, &workflow.SessionOptions{
					ExecutionTimeout: work.SessionExecutionTimeout,
					CreationTimeout:  work.SessionCreationTimeout,
				})
				if err != nil {
					if input.ToolTarget.Kind == agent.ToolTargetRunWorker || errors.Is(err, workflow.ErrSessionFailed) {
						return terminalAgentFailure(result, agent.TerminalFailureSessionLost)
					}
					return result, fmt.Errorf("create agent tool session on %q: %w", toolTaskQueue, err)
				}
				sessionContext = created
				defer workflow.CompleteSession(sessionContext)
			}
			toolContext := workflow.WithActivityOptions(sessionContext, agentToolActivityOptions())
			for _, call := range turn.ToolCalls {
				var toolOutput agent.ToolOutput
				if err := workflow.ExecuteActivity(toolContext, agent.ToolActivityName, agent.ToolInput{
					ToolsetID: input.ToolsetID, ToolsetFingerprint: state.ToolsetFingerprint,
					ConversationRef: conversationRef, TranscriptRef: result.TranscriptRef, Call: call,
				}).Get(ctx, &toolOutput); err != nil {
					if failure := toolTerminalFailure(err); failure != nil {
						return terminalAgentFailure(result, failure.Kind)
					}
					return result, fmt.Errorf("run agent tool %q: %w", call.Name, err)
				}
				if toolOutput.CallID != call.CallID {
					return terminalAgentFailure(result, agent.TerminalFailureInvalidProviderOutcome)
				}
				conversationRef = toolOutput.ConversationRef
				result.ConversationRef = conversationRef
				if toolOutput.TranscriptRef.Key != "" {
					result.TranscriptRef = toolOutput.TranscriptRef
				}
				result.ToolCalls++
				if !unbounded && conversationRef.Bytes > input.LegacyLimits.MaxConversationBytes {
					return terminalLegacyAgentBudgetFailure(result, "conversation_bytes")
				}
			}
			continue
		case agent.OutcomeFinalText:
		default:
			return terminalAgentFailure(result, agent.TerminalFailureInvalidProviderOutcome)
		}
		var finalized agentactivities.FinalizeOutput
		if err := workflow.ExecuteActivity(controlContext, agent.FinalizeActivityName, agentactivities.FinalizeInput{
			Stage: input.Attempt.Key.Stage, TextRef: turn.FinalTextRef, TranscriptRef: result.TranscriptRef,
		}).Get(ctx, &finalized); err != nil {
			if failure := modelTerminalFailure(err); failure != nil {
				return terminalAgentFailure(result, failure.Kind)
			}
			return result, fmt.Errorf("finalize agent output: %w", err)
		}
		if finalized.Result == nil || finalized.Result.Value() == nil {
			return terminalAgentFailure(result, agent.TerminalFailureInvalidProviderOutcome)
		}
		result.Result = *finalized.Result
		if finalized.TranscriptRef.Key != "" {
			result.TranscriptRef = finalized.TranscriptRef
		}
		return result, nil
	}
}

func terminalAgentFailure(result AgentWorkflowResult, kind agent.TerminalFailureKind) (AgentWorkflowResult, error) {
	result.Failure = &agent.TerminalFailure{Kind: kind}
	return result, nil
}

func terminalLegacyAgentBudgetFailure(result AgentWorkflowResult, budget string) (AgentWorkflowResult, error) {
	result.Failure = &agent.TerminalFailure{Kind: agent.TerminalFailureKind("budget_exhausted"), Budget: budget}
	return result, nil
}

func modelTerminalFailure(err error) *agent.TerminalFailure {
	var timeoutError *temporal.TimeoutError
	if errors.As(err, &timeoutError) {
		return &agent.TerminalFailure{Kind: agent.TerminalFailureModelExhausted}
	}
	var applicationError *temporal.ApplicationError
	if !errors.As(err, &applicationError) {
		return nil
	}
	switch applicationError.Type() {
	case agent.ErrorTypeRateLimit:
		return &agent.TerminalFailure{Kind: agent.TerminalFailureRateLimited}
	case agent.ErrorTypeAuth:
		return &agent.TerminalFailure{Kind: agent.TerminalFailureAuthentication}
	case agent.ErrorTypeTransient:
		return &agent.TerminalFailure{Kind: agent.TerminalFailureModelExhausted}
	case agent.ErrorTypeInvalidProviderOutcome:
		return &agent.TerminalFailure{Kind: agent.TerminalFailureInvalidProviderOutcome}
	default:
		return nil
	}
}

func toolTerminalFailure(err error) *agent.TerminalFailure {
	if errors.Is(err, workflow.ErrSessionFailed) {
		return &agent.TerminalFailure{Kind: agent.TerminalFailureSessionLost}
	}
	var applicationError *temporal.ApplicationError
	if errors.As(err, &applicationError) {
		switch applicationError.Type() {
		case agent.ErrorTypeSessionLost:
			return &agent.TerminalFailure{Kind: agent.TerminalFailureSessionLost}
		case agent.ErrorTypeAmbiguousToolExecution:
			return &agent.TerminalFailure{Kind: agent.TerminalFailureAmbiguousToolExecution}
		}
	}
	return nil
}

func agentModelTurnActivityOptions(policy work.AgentActivityPolicy) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    policy.StartToCloseTimeout,
		ScheduleToCloseTimeout: policy.ScheduleToCloseTimeout,
		HeartbeatTimeout:       policy.HeartbeatTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: policy.Retry.InitialInterval, BackoffCoefficient: policy.Retry.BackoffCoefficient,
			MaximumInterval: policy.Retry.MaximumInterval, MaximumAttempts: policy.Retry.MaximumAttempts,
		},
	}
}

func agentControlActivityOptions(policy work.ActivityPolicy) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: policy.StartToCloseTimeout, ScheduleToCloseTimeout: policy.ScheduleToCloseTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: policy.Retry.InitialInterval, BackoffCoefficient: policy.Retry.BackoffCoefficient,
			MaximumInterval: policy.Retry.MaximumInterval, MaximumAttempts: policy.Retry.MaximumAttempts,
		},
	}
}

func agentToolActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: agent.MaxToolExecutionDuration + agent.ToolActivityPersistenceMargin,
		HeartbeatTimeout:    15 * time.Second,
		WaitForCancellation: true,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	}
}

func recordAgentLifecycle(ctx workflow.Context, terminalErr error, failure *agent.TerminalFailure) {
	input, record := agentLifecycleInput(terminalErr, failure)
	if !record {
		return
	}
	disconnected, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	disconnected = workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 5 * time.Second, MaximumAttempts: 3,
		},
	})
	if err := workflow.ExecuteActivity(disconnected, agent.LifecycleActivityName, input).Get(disconnected, nil); err != nil {
		workflow.GetLogger(disconnected).Error("record agent lifecycle", "error", err)
	}
}

func agentLifecycleInput(terminalErr error, failure *agent.TerminalFailure) (agentactivities.LifecycleInput, bool) {
	if workflow.IsContinueAsNewError(terminalErr) {
		return agentactivities.LifecycleInput{}, false
	}
	if terminalErr == nil {
		if failure != nil {
			return agentactivities.LifecycleInput{Outcome: telemetry.AgentOutcomeFailed, Budget: failure.Budget}, true
		}
		return agentactivities.LifecycleInput{Outcome: telemetry.AgentOutcomeSucceeded}, true
	}
	if temporal.IsCanceledError(terminalErr) {
		return agentactivities.LifecycleInput{Outcome: telemetry.AgentOutcomeCancelled}, true
	}
	input := agentactivities.LifecycleInput{Outcome: telemetry.AgentOutcomeFailed}
	var applicationError *temporal.ApplicationError
	if errors.As(terminalErr, &applicationError) {
		input.Budget = map[string]string{
			"AgentModelTurnBudget":    "model_turns",
			"AgentToolCallBudget":     "tool_calls",
			"AgentInputTokenBudget":   "input_tokens",
			"AgentOutputTokenBudget":  "output_tokens",
			"AgentConversationBudget": "conversation_bytes",
		}[applicationError.Type()]
	}
	return input, true
}

func continueAgentWorkflowAsNew(ctx workflow.Context, input AgentWorkflowInput, state AgentWorkflowState, unbounded bool) error {
	legacyLimits := input.LegacyLimits
	if unbounded {
		legacyLimits = nil
	}
	return workflow.NewContinueAsNewError(ctx, AgentWorkflow, AgentWorkflowInput{
		Attempt: activities.StageAttempt{
			Key: input.Attempt.Key, Model: input.Attempt.Model,
		},
		ToolsetID:       input.ToolsetID,
		ToolTarget:      input.ToolTarget,
		LegacyLimits:    legacyLimits,
		ModelTurnPolicy: input.ModelTurnPolicy,
		ControlPolicy:   input.ControlPolicy,
		Identity:        input.Identity,
		CacheKey:        input.CacheKey,
		Seed:            input.Seed,
		State:           &state,
	})
}

func validateAgentInput(input AgentWorkflowInput) error {
	if input.Attempt.Key.RunID == "" || input.Attempt.Key.Stage == "" || input.Attempt.Key.Turn < 1 ||
		input.ToolsetID == "" || input.CacheKey == "" {
		return fmt.Errorf("agent workflow identity, stage, turn, toolset, and cache key are required")
	}
	if err := input.Attempt.Model.Validate(); err != nil {
		return fmt.Errorf("validate agent workflow model: %w", err)
	}
	if _, err := input.ToolTarget.TaskQueue(input.Attempt.Key.RunID); err != nil {
		return fmt.Errorf("validate agent workflow tool target: %w", err)
	}
	if err := input.ModelTurnPolicy.Validate(); err != nil {
		return fmt.Errorf("validate agent workflow model-turn policy: %w", err)
	}
	if err := input.ControlPolicy.Validate("agent control"); err != nil {
		return fmt.Errorf("validate agent workflow control policy: %w", err)
	}
	if _, err := agent.ConversationIdentity(input.Identity, input.Attempt.Key); err != nil {
		return fmt.Errorf("validate agent workflow conversation identity: %w", err)
	}
	if input.Seed != nil {
		if err := input.Seed.ValidateFor(input.Attempt.Key); err != nil {
			return fmt.Errorf("validate agent workflow conversation seed: %w", err)
		}
	}
	if input.State != nil {
		if input.State.ModelTurns < 0 || input.State.ToolCalls < 0 {
			return fmt.Errorf("agent workflow continued counters must not be negative")
		}
		if input.State.ToolsetFingerprint == "" {
			return fmt.Errorf("agent workflow continued state needs a pinned toolset fingerprint")
		}
	}
	return nil
}
