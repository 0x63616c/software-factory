// Package sf provides shared domain types and a typed HTTP client used by both
// the sf CLI and sf TUI.
package sf

import "strings"

// OutputFormat controls `sf` serialization.
type OutputFormat string

const (
	// OutputFormatTable renders the default compact table.
	OutputFormatTable OutputFormat = "table"
	// OutputFormatJSON renders machine-readable JSON.
	OutputFormatJSON OutputFormat = "json"
	// OutputFormatYAML renders machine-readable YAML.
	OutputFormatYAML OutputFormat = "yaml"
	// OutputFormatWide renders the expanded table.
	OutputFormatWide OutputFormat = "wide"
)

var allowedOutputFormats = map[OutputFormat]struct{}{
	OutputFormatTable: {},
	OutputFormatJSON:  {},
	OutputFormatYAML:  {},
	OutputFormatWide:  {},
}

// TicketState defines a normalized ticket lifecycle state.
type TicketState string

const (
	// TicketStateOpen is ready for Dispatcher admission.
	TicketStateOpen TicketState = "open"
	// TicketStateActive is currently being worked.
	TicketStateActive TicketState = "active"
	// TicketStateFailed needs intervention or retry.
	TicketStateFailed TicketState = "failed"
	// TicketStateDone has completed successfully.
	TicketStateDone TicketState = "done"
)

var allowedTicketStates = map[TicketState]struct{}{
	TicketStateOpen:   {},
	TicketStateActive: {},
	TicketStateFailed: {},
	TicketStateDone:   {},
}

// ParseOutputFormat returns a stable output kind for shared command parsing.
func ParseOutputFormat(raw string) OutputFormat {
	switch raw {
	case string(OutputFormatJSON):
		return OutputFormatJSON
	case string(OutputFormatYAML):
		return OutputFormatYAML
	case string(OutputFormatTable):
		return OutputFormatTable
	case string(OutputFormatWide):
		return OutputFormatWide
	default:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case string(OutputFormatJSON):
			return OutputFormatJSON
		case string(OutputFormatYAML):
			return OutputFormatYAML
		case string(OutputFormatTable):
			return OutputFormatTable
		case string(OutputFormatWide):
			return OutputFormatWide
		default:
			return OutputFormatTable
		}
	}
}

// ParseOutputFormatStrict returns an error when the mode is unsupported.
func ParseOutputFormatStrict(raw string) (OutputFormat, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	switch format {
	case "", string(OutputFormatJSON), string(OutputFormatYAML), string(OutputFormatTable), string(OutputFormatWide):
		if format == "" {
			return OutputFormatTable, nil
		}
		return OutputFormat(format), nil
	default:
		return OutputFormatTable, ErrInvalidOutputFormat{OutputFormat: raw}
	}
}

// IsValidOutputFormat reports whether format is one of the supported output modes.
func IsValidOutputFormat(format OutputFormat) bool {
	_, ok := allowedOutputFormats[format]
	return ok
}

// IsValidTicketState reports whether state is one of the supported ticket states.
func IsValidTicketState(raw string) bool {
	state := TicketState(strings.ToLower(strings.TrimSpace(raw)))
	_, ok := allowedTicketStates[state]
	return ok
}

// ErrInvalidTicketState is returned for unsupported ticket states.
type ErrInvalidTicketState struct {
	State string
}

func (err ErrInvalidTicketState) Error() string {
	return "invalid ticket state: " + err.State
}

// ErrInvalidOutputFormat is returned when output mode cannot be mapped.
type ErrInvalidOutputFormat struct {
	OutputFormat string
}

func (err ErrInvalidOutputFormat) Error() string {
	return "invalid output mode: " + err.OutputFormat
}

// TicketSummary is the list representation returned by the API.
type TicketSummary struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Ready     bool   `json:"ready"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// TicketResponse is one Ticket with dependency edges.
type TicketResponse struct {
	ID        int64           `json:"id"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	State     string          `json:"state"`
	Ready     bool            `json:"ready"`
	Blockers  []TicketSummary `json:"blockers"`
	Blocks    []TicketSummary `json:"blocks"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
}

// AttemptOutput is one durable semantic attempt of a Step.
type AttemptOutput struct {
	AttemptNo     int     `json:"attemptNo"`
	AgentStage    string  `json:"agentStage"`
	Model         string  `json:"model"`
	Effort        string  `json:"effort"`
	State         string  `json:"state"`
	FailureKind   string  `json:"failureKind"`
	ExecutionID   string  `json:"executionId"`
	UsageState    string  `json:"usageState"`
	Measured      bool    `json:"measured"`
	InputTokens   *int64  `json:"inputTokens"`
	CachedInput   *int64  `json:"cachedInputTokens"`
	OutputTokens  *int64  `json:"outputTokens"`
	Reasoning     *int64  `json:"reasoningTokens"`
	HasTranscript bool    `json:"hasTranscript"`
	TranscriptURL string  `json:"transcriptPath"`
	StartedAt     string  `json:"startedAt"`
	EndedAt       *string `json:"endedAt"`
	Result        []byte  `json:"result"`
}

// StepOutput is one durable target pipeline Step.
type StepOutput struct {
	Ordinal   int             `json:"ordinal"`
	Kind      string          `json:"kind"`
	Iteration int             `json:"iteration"`
	Reason    string          `json:"reason"`
	State     string          `json:"state"`
	Usage     UsageOutput     `json:"usage"`
	StartedAt string          `json:"startedAt"`
	EndedAt   *string         `json:"endedAt"`
	Result    []byte          `json:"result"`
	Attempts  []AttemptOutput `json:"attempts"`
}

// UsageOutput is the token usage summary shared across attempts, steps and runs.
type UsageOutput struct {
	InputTokens       int64 `json:"inputTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	ReasoningTokens   int64 `json:"reasoningTokens"`
	Complete          bool  `json:"complete"`
}

// ConfirmedMergeOutput records the reviewed head and resulting merge commit.
type ConfirmedMergeOutput struct {
	ReviewedHead string `json:"reviewedHead"`
	MergeSHA     string `json:"mergeSha"`
}

// RunOutput is one full target Run attempt.
type RunOutput struct {
	ID             string                `json:"id"`
	TicketID       int64                 `json:"ticketId"`
	StartedAt      string                `json:"startedAt"`
	EndedAt        *string               `json:"endedAt"`
	Outcome        string                `json:"outcome"`
	FailureKind    string                `json:"failureKind"`
	Active         bool                  `json:"active"`
	Phase          string                `json:"phase"`
	ConfirmedMerge *ConfirmedMergeOutput `json:"confirmedMerge,omitempty"`
	Steps          []StepOutput          `json:"steps"`
	Usage          UsageOutput           `json:"usage"`
}

// ErrorResponse mirrors the API's typed error schema.
type ErrorResponse struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`
	Type   string `json:"type"`
}
