package main

import (
	"os"
	"strings"
	"testing"
)

func TestRegisterExposesOnlyTheGenericAgentToolActivity(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := toolWorkerRegisterBody(t, string(source))
	if !strings.Contains(body, "agent.ToolActivityName") {
		t.Fatal("Tool Worker register() does not name the generic agent tool activity")
	}
	for _, forbidden := range []string{"RunPlan", "RunImplement", "RunReview", "RegisterWorkflow"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Tool Worker register() exposes forbidden production registration %q", forbidden)
		}
	}
}

func toolWorkerRegisterBody(t *testing.T, source string) string {
	t.Helper()
	start := strings.Index(source, "func register(")
	if start < 0 {
		t.Fatal("main.go has no register function")
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of register")
	}
	return source[start : start+end]
}
