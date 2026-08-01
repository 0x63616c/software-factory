package sf

import "testing"

func TestParseOutputFormatStrict(t *testing.T) {
	t.Run("valid wide", func(t *testing.T) {
		format, err := ParseOutputFormatStrict("wide")
		if err != nil {
			t.Fatalf("expected wide to be valid, got error: %v", err)
		}
		if format != OutputFormatWide {
			t.Fatalf("expected wide format, got %q", format)
		}
	})

	t.Run("valid JSON", func(t *testing.T) {
		format, err := ParseOutputFormatStrict("json")
		if err != nil {
			t.Fatalf("expected json to be valid, got error: %v", err)
		}
		if format != OutputFormatJSON {
			t.Fatalf("expected json format, got %q", format)
		}
	})

	t.Run("invalid value", func(t *testing.T) {
		if _, err := ParseOutputFormatStrict("bad"); err == nil {
			t.Fatal("expected invalid output format error")
		}
	})
}

func TestTicketStateValidation(t *testing.T) {
	valid := []string{"open", "active", "failed", "done", "OPEN"}
	for _, state := range valid {
		if !IsValidTicketState(state) {
			t.Fatalf("expected %q to be valid", state)
		}
	}
	invalid := []string{"", "closed", "running", "pending"}
	for _, state := range invalid {
		if IsValidTicketState(state) {
			t.Fatalf("expected %q to be invalid", state)
		}
	}
}

func TestErrInvalidTicketState(t *testing.T) {
	err := ErrInvalidTicketState{State: "closed"}
	if err.Error() != "invalid ticket state: closed" {
		t.Fatalf("unexpected error message: %q", err.Error())
	}
}
