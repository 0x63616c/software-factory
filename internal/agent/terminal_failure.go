package agent

import "fmt"

// TerminalFailureKind identifies an expected terminal AgentWorkflow outcome
// that a parent can handle without parsing an SDK or provider error string.
type TerminalFailureKind string

// BudgetKind identifies the declared resource limit that ended an agent.
type BudgetKind string

const (
	// BudgetModelTurns limits direct provider turns.
	BudgetModelTurns BudgetKind = "model_turns"
	// BudgetToolCalls limits sandbox tool calls.
	BudgetToolCalls BudgetKind = "tool_calls"
	// BudgetInputTokens limits provider input tokens.
	BudgetInputTokens BudgetKind = "input_tokens"
	// BudgetOutputTokens limits provider output tokens.
	BudgetOutputTokens BudgetKind = "output_tokens"
	// BudgetConversationBytes limits retained provider-neutral context.
	BudgetConversationBytes BudgetKind = "conversation_bytes"
)

const (
	// TerminalFailureSessionLost means the tool worker Session became unavailable.
	TerminalFailureSessionLost TerminalFailureKind = "session_lost"
	// TerminalFailureAmbiguousToolExecution means a tool might have changed state before interruption.
	TerminalFailureAmbiguousToolExecution TerminalFailureKind = "ambiguous_tool_execution"
	// TerminalFailureModelExhausted means the direct model activity exhausted its retry or timeout policy.
	TerminalFailureModelExhausted TerminalFailureKind = "model_exhausted"
	// TerminalFailureRateLimited means the provider refused the authorized model call for capacity.
	TerminalFailureRateLimited TerminalFailureKind = "rate_limited"
	// TerminalFailureAuthentication means the provider credential needs repair.
	TerminalFailureAuthentication TerminalFailureKind = "authentication"
	// TerminalFailureBudgetExhausted means the child spent one of its declared budgets.
	TerminalFailureBudgetExhausted TerminalFailureKind = "budget_exhausted"
	// TerminalFailureInvalidProviderOutcome means the provider result violated the typed contract.
	TerminalFailureInvalidProviderOutcome TerminalFailureKind = "invalid_provider_outcome"
)

// TerminalFailure is the bounded, provider-neutral terminal handoff from an
// AgentWorkflow to its parent.
type TerminalFailure struct {
	Kind   TerminalFailureKind `json:"kind"`
	Budget BudgetKind          `json:"budget,omitempty"`
}

// Validate confirms the failure can cross the workflow boundary. The fixed
// vocabulary makes parents exhaustive without depending on provider errors.
func (failure TerminalFailure) Validate() error {
	switch failure.Kind {
	case TerminalFailureSessionLost,
		TerminalFailureAmbiguousToolExecution,
		TerminalFailureModelExhausted,
		TerminalFailureRateLimited,
		TerminalFailureAuthentication,
		TerminalFailureInvalidProviderOutcome:
		if failure.Budget != "" {
			return fmt.Errorf("terminal failure %q cannot name a budget", failure.Kind)
		}
		return nil
	case TerminalFailureBudgetExhausted:
		switch failure.Budget {
		case BudgetModelTurns, BudgetToolCalls, BudgetInputTokens, BudgetOutputTokens, BudgetConversationBytes:
			return nil
		case "":
			return fmt.Errorf("budget-exhausted terminal failure requires a budget")
		default:
			return fmt.Errorf("unknown terminal failure budget %q", failure.Budget)
		}
	default:
		return fmt.Errorf("unknown terminal failure kind %q", failure.Kind)
	}
}

// IsBudgetExhausted reports whether failure names a particular budget, or any
// budget when kind is empty.
func (failure *TerminalFailure) IsBudgetExhausted(kind BudgetKind) bool {
	return failure != nil && failure.Kind == TerminalFailureBudgetExhausted && (kind == "" || failure.Budget == kind)
}

// Is reports whether failure has kind. It is nil-safe for parent decision branches.
func (failure *TerminalFailure) Is(kind TerminalFailureKind) bool {
	return failure != nil && failure.Kind == kind
}
