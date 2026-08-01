package agent

import "fmt"

// TerminalFailureKind identifies an expected terminal AgentWorkflow outcome
// that a parent can handle without parsing an SDK or provider error string.
type TerminalFailureKind string

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
	// TerminalFailureInvalidProviderOutcome means the provider result violated the typed contract.
	TerminalFailureInvalidProviderOutcome TerminalFailureKind = "invalid_provider_outcome"
)

// TerminalFailure is the provider-neutral terminal handoff from an
// AgentWorkflow to its parent.
type TerminalFailure struct {
	Kind TerminalFailureKind `json:"kind"`
	// Budget decodes pre-unbounded workflow results during their retention
	// window. New executions never populate it.
	Budget string `json:"budget,omitempty"`
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
	case TerminalFailureKind("budget_exhausted"):
		if failure.Budget == "" {
			return fmt.Errorf("legacy budget-exhausted terminal failure requires its recorded budget")
		}
		return nil
	default:
		return fmt.Errorf("unknown terminal failure kind %q", failure.Kind)
	}
}

// Is reports whether failure has kind. It is nil-safe for parent decision branches.
func (failure *TerminalFailure) Is(kind TerminalFailureKind) bool {
	return failure != nil && failure.Kind == kind
}
