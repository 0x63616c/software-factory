package codexresponses

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/0x63616c/software-factory/internal/work"
)

// Credential is the bearer credential and ChatGPT account selected for a call.
type Credential struct {
	AccessToken work.Credential
	AccountID   string
}

// CredentialSource supplies a current credential without exposing storage details.
type CredentialSource interface {
	Credential(ctx context.Context) (Credential, error)
}

// ToolChoice controls whether the model may emit tool calls.
type ToolChoice string

const (
	// ToolChoiceNone prevents tool calls.
	ToolChoiceNone ToolChoice = "none"
	// ToolChoiceAuto lets the model decide whether to call a tool.
	ToolChoiceAuto ToolChoice = "auto"
	// ToolChoiceRequired requires at least one tool call.
	ToolChoiceRequired ToolChoice = "required"
)

// TextVerbosity controls the density of the final answer.
type TextVerbosity string

const (
	// TextVerbosityLow asks for terse output.
	TextVerbosityLow TextVerbosity = "low"
	// TextVerbosityMedium asks for normal output.
	TextVerbosityMedium TextVerbosity = "medium"
	// TextVerbosityHigh asks for detailed output.
	TextVerbosityHigh TextVerbosity = "high"
)

// ResponseFormat requests one strict JSON-schema final response.
type ResponseFormat struct {
	Name   string
	Schema json.RawMessage
}

// ReasoningEffort controls how much reasoning the model may perform.
type ReasoningEffort string

const (
	// ReasoningEffortLow requests a small reasoning budget.
	ReasoningEffortLow ReasoningEffort = "low"
	// ReasoningEffortMedium requests the normal reasoning budget.
	ReasoningEffortMedium ReasoningEffort = "medium"
	// ReasoningEffortHigh requests a larger reasoning budget.
	ReasoningEffortHigh ReasoningEffort = "high"
	// ReasoningEffortXHigh requests the largest generally available reasoning budget.
	ReasoningEffortXHigh ReasoningEffort = "xhigh"
)

// ReasoningSummary controls whether the provider returns a reasoning summary.
type ReasoningSummary string

const (
	// ReasoningSummaryAuto lets the provider choose summary detail.
	ReasoningSummaryAuto ReasoningSummary = "auto"
	// ReasoningSummaryConcise requests a concise summary.
	ReasoningSummaryConcise ReasoningSummary = "concise"
	// ReasoningSummaryDetailed requests a detailed summary.
	ReasoningSummaryDetailed ReasoningSummary = "detailed"
)

// ReasoningOptions configure reasoning for a turn.
type ReasoningOptions struct {
	Effort  ReasoningEffort  `json:"effort"`
	Summary ReasoningSummary `json:"summary"`
}

// Tool describes one function the model may ask the caller to execute.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// InputType identifies one item supplied to a model turn.
type InputType string

const (
	// InputUserText is a user-authored text message.
	InputUserText InputType = "user_text"
	// InputAssistantText replays one prior assistant-authored text message.
	InputAssistantText InputType = "assistant_text"
	// InputFunctionOutput returns one allowlisted tool's result to the model.
	InputFunctionOutput InputType = "function_output"
	// InputFunctionCall replays one prior assistant tool call for stateless SSE.
	InputFunctionCall InputType = "function_call"
)

// InputItem is one typed item in a turn's conversation input.
type InputItem struct {
	Type      InputType
	Text      string
	CallID    string
	Output    string
	Name      string
	Arguments json.RawMessage
}

// FunctionOutput constructs the continuation item for a completed tool call.
func FunctionOutput(callID, output string) InputItem {
	return InputItem{Type: InputFunctionOutput, CallID: callID, Output: output}
}

// FunctionCall constructs a compact replay item for a prior assistant call.
func FunctionCall(call ToolCall) InputItem {
	return InputItem{
		Type: InputFunctionCall, CallID: call.CallID, Name: call.Name, Arguments: call.Arguments,
	}
}

// UserText constructs a user text input.
func UserText(text string) InputItem {
	return InputItem{Type: InputUserText, Text: text}
}

// AssistantText constructs a prior assistant text input.
func AssistantText(text string) InputItem {
	return InputItem{Type: InputAssistantText, Text: text}
}

// TurnRequest describes one direct Codex Responses turn.
type TurnRequest struct {
	Model              string
	Instructions       string
	Input              []InputItem
	Store              bool
	Tools              []Tool
	ToolChoice         ToolChoice
	ParallelToolCalls  bool
	Reasoning          ReasoningOptions
	TextVerbosity      TextVerbosity
	ResponseFormat     *ResponseFormat
	PromptCacheKey     string
	IdempotencyKey     string
	PreviousResponseID string
	Include            []string
}

// Outcome distinguishes a final answer from a turn requiring tool execution.
type Outcome string

const (
	// OutcomeFinalText means the turn produced a terminal assistant answer.
	OutcomeFinalText Outcome = "final_text"
	// OutcomeToolCalls means the turn requires one or more tools.
	OutcomeToolCalls Outcome = "tool_calls"
)

// Usage records provider token accounting for one completed turn.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// ToolCall is one complete function invocation requested by the model.
type ToolCall struct {
	ID        string
	CallID    string
	Name      string
	Arguments json.RawMessage
}

// TurnResult is the durable, provider-neutral result of one model turn.
type TurnResult struct {
	Outcome    Outcome
	ResponseID string
	Text       string
	ToolCalls  []ToolCall
	Status     string
	Usage      Usage
}

// EventType identifies a transient stream event kept out of Temporal history.
type EventType string

const (
	// EventTextDelta carries incremental final-answer text.
	EventTextDelta EventType = "text_delta"
	// EventReasoningDelta carries transient reasoning-summary progress.
	EventReasoningDelta EventType = "reasoning_delta"
)

// Event is transient progress emitted while a turn is running.
type Event struct {
	Type  EventType
	Delta string
}

// EmitFunc receives transient progress. It must not retain credentials.
type EmitFunc func(Event)

// ErrStreamInterrupted marks an SSE stream that ended without a terminal event.
var ErrStreamInterrupted = errors.New("codex responses stream interrupted")
