package agent

import "testing"

func TestTerminalFailureValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		failure TerminalFailure
		valid   bool
	}{
		{name: "session lost", failure: TerminalFailure{Kind: TerminalFailureSessionLost}, valid: true},
		{name: "budget", failure: TerminalFailure{Kind: TerminalFailureBudgetExhausted, Budget: BudgetToolCalls}, valid: true},
		{name: "unknown kind", failure: TerminalFailure{Kind: "unknown"}},
		{name: "budget missing its name", failure: TerminalFailure{Kind: TerminalFailureBudgetExhausted}},
		{name: "budget with an unknown name", failure: TerminalFailure{Kind: TerminalFailureBudgetExhausted, Budget: "unknown"}},
		{name: "non budget with a budget", failure: TerminalFailure{Kind: TerminalFailureSessionLost, Budget: BudgetToolCalls}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.failure.Validate()
			if (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, test.valid)
			}
		})
	}
}
