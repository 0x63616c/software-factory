package agent

import (
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

const (
	// MaxToolExecutionDuration is the longest one model-selected command may
	// run. The enclosing Temporal activity must outlive this bound so it can
	// persist the terminal marker instead of timing out an otherwise valid
	// command and turning its retry into an ambiguous execution.
	MaxToolExecutionDuration = 30 * time.Minute
	// ToolActivityPersistenceMargin leaves time to persist bounded output and
	// the completion marker after the command itself reaches its limit.
	ToolActivityPersistenceMargin = time.Minute
)

// ToolsetID identifies one immutable meaning of a tool catalogue.
type ToolsetID string

// ToolTargetKind identifies the generation-affine worker that executes tools.
type ToolTargetKind string

const (
	// ToolTargetRunWorker routes tools to one validated Run Worker generation.
	ToolTargetRunWorker ToolTargetKind = "run_worker"
)

// ToolTarget is the durable tool-routing decision pinned at child-workflow
// start. Only a concrete Run Worker generation is valid after activation.
type ToolTarget struct {
	Kind              ToolTargetKind         `json:"kind"`
	RunWorkerIdentity work.RunWorkerIdentity `json:"run_worker_identity"`
}

// TaskQueue resolves the target without consulting mutable external state.
func (target ToolTarget) TaskQueue(runID string) (string, error) {
	switch target.Kind {
	case ToolTargetRunWorker:
		if target.RunWorkerIdentity.RunID != runID {
			return "", fmt.Errorf("run worker tool target belongs to Run %q, not %q: %w", target.RunWorkerIdentity.RunID, runID, work.ErrInvalidRun)
		}
		queue, err := work.RunWorkerToolTaskQueue(target.RunWorkerIdentity)
		if err != nil {
			return "", fmt.Errorf("resolve Run Worker tool target: %w", err)
		}
		return queue, nil
	default:
		return "", fmt.Errorf("unknown agent tool target %q: %w", target.Kind, work.ErrInvalidRun)
	}
}

const (
	// ToolsetCodingReadV1 is the immutable read-only plan/review catalogue.
	ToolsetCodingReadV1 ToolsetID = "coding-read-v1"
	// ToolsetCodingWriteV1 is the immutable implement catalogue.
	ToolsetCodingWriteV1 ToolsetID = "coding-write-v1"
)

// Limits fixes one agent run's resource budgets at child-workflow start.
type Limits struct {
	MaxModelTurns        int   `json:"max_model_turns"`
	MaxToolCalls         int   `json:"max_tool_calls"`
	MaxInputTokens       int64 `json:"max_input_tokens"`
	MaxOutputTokens      int64 `json:"max_output_tokens"`
	MaxConversationBytes int64 `json:"max_conversation_bytes"`
	ContinueAsNewAfter   int   `json:"continue_as_new_after"`
}

// DefaultLimits returns the fixed V1 operational and spend bounds for one stage agent.
func DefaultLimits() Limits {
	return Limits{
		MaxModelTurns: 24, MaxToolCalls: 96, MaxInputTokens: 500_000, MaxOutputTokens: 100_000,
		MaxConversationBytes: 1 << 20, ContinueAsNewAfter: 8,
	}
}

// ModelTurnInput routes one provider turn using only bounded metadata and a conversation reference.
type ModelTurnInput struct {
	Model              work.Model        `json:"model"`
	ToolsetID          ToolsetID         `json:"toolset_id"`
	ToolsetFingerprint string            `json:"toolset_fingerprint"`
	ConversationRef    ConversationRef   `json:"conversation_ref"`
	TranscriptRef      TranscriptRef     `json:"transcript_ref"`
	ResponseFormat     ResponseFormatRef `json:"response_format"`
	PromptCacheKey     string            `json:"prompt_cache_key"`
	ModelTurn          int               `json:"model_turn"`
	IdempotencyKey     string            `json:"idempotency_key"`
}

// TurnOutcome distinguishes a terminal answer from requested tool calls.
type TurnOutcome string

const (
	// OutcomeFinalText means the model produced its terminal structured text.
	OutcomeFinalText TurnOutcome = "final_text"
	// OutcomeToolCalls means the model requested one or more tools.
	OutcomeToolCalls TurnOutcome = "tool_calls"
)

// ArtifactRef identifies one immutable agent artifact.
type ArtifactRef struct {
	Key    string `json:"key"`
	Bytes  int64  `json:"bytes"`
	Digest string `json:"digest"`
}

// TextRef identifies immutable final text.
type TextRef ArtifactRef

// ArgumentsRef identifies immutable tool arguments.
type ArgumentsRef ArtifactRef

// OutputRef identifies immutable oversized tool output.
type OutputRef ArtifactRef

// TranscriptRef identifies the latest immutable revision of one provider-neutral transcript.
type TranscriptRef struct {
	Key      string `json:"key"`
	Revision int    `json:"revision"`
	Bytes    int64  `json:"bytes"`
	Digest   string `json:"digest"`
}

// ResponseSchemaRef identifies one immutable provider structured-output schema.
type ResponseSchemaRef ArtifactRef

// ResponseFormatRef names the strict structured output expected from every model turn.
type ResponseFormatRef struct {
	Name      string            `json:"name"`
	SchemaRef ResponseSchemaRef `json:"schema_ref"`
}

// PendingToolCall is bounded routing metadata for one provider function call.
type PendingToolCall struct {
	CallID       string       `json:"call_id"`
	Name         string       `json:"name"`
	ArgumentsRef ArgumentsRef `json:"arguments_ref"`
}

// ToolInput routes one pending model call to the Run Worker tool activity.
type ToolInput struct {
	ToolsetID          ToolsetID       `json:"toolset_id"`
	ToolsetFingerprint string          `json:"toolset_fingerprint"`
	ConversationRef    ConversationRef `json:"conversation_ref"`
	TranscriptRef      TranscriptRef   `json:"transcript_ref"`
	Call               PendingToolCall `json:"call"`
}

// ToolOutput is the bounded durable result of one Run Worker tool activity.
type ToolOutput struct {
	CallID          string          `json:"call_id"`
	ConversationRef ConversationRef `json:"conversation_ref"`
	TranscriptRef   TranscriptRef   `json:"transcript_ref"`
	IsError         bool            `json:"is_error"`
}

// ModelTurnResult is the bounded durable outcome of one provider activity.
type ModelTurnResult struct {
	Outcome            TurnOutcome       `json:"outcome"`
	ToolsetFingerprint string            `json:"toolset_fingerprint"`
	ConversationRef    ConversationRef   `json:"conversation_ref"`
	TranscriptRef      TranscriptRef     `json:"transcript_ref"`
	FinalTextRef       TextRef           `json:"final_text_ref"`
	ToolCalls          []PendingToolCall `json:"tool_calls"`
	Usage              work.Usage        `json:"usage"`
	UsageMeasured      bool              `json:"usage_measured"`
}
