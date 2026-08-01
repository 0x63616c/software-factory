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
		{name: "legacy budget replay", failure: TerminalFailure{Kind: TerminalFailureKind("budget_exhausted"), Budget: "tool_calls"}, valid: true},
		{name: "unknown kind", failure: TerminalFailure{Kind: "unknown"}},
		{name: "legacy budget missing its recorded name", failure: TerminalFailure{Kind: TerminalFailureKind("budget_exhausted")}},
		{name: "non budget with a budget", failure: TerminalFailure{Kind: TerminalFailureSessionLost, Budget: "tool_calls"}},
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
