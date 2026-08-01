//go:build e2e

package e2e

import (
	"slices"
	"testing"
)

func TestTicketCompletesThroughTheDurableAgentWorkflow(t *testing.T) {
	result := runE2E(t)

	if result.TicketState != "done" || result.RunOutcome != "succeeded" {
		t.Fatalf("terminal lifecycle = ticket %q, run %q", result.TicketState, result.RunOutcome)
	}
	if !slices.Equal(result.AgentWorkflowStages, []string{"plan", "implement", "review"}) {
		t.Fatalf("AgentWorkflow stages = %v", result.AgentWorkflowStages)
	}
	if result.Merge.Method != "squash" || !result.Merge.ReviewedHeadMatched {
		t.Fatalf("merge evidence = %+v", result.Merge)
	}
	if result.ActiveRuns != 0 || result.RemainingRunWorkers != 0 {
		t.Fatalf("cleanup = %d active Runs, %d Run Workers", result.ActiveRuns, result.RemainingRunWorkers)
	}
	if result.ModelAdapter != "fake-responses" || result.GitHubAdapter != "fake" {
		t.Fatalf("external adapters = model %q, GitHub %q", result.ModelAdapter, result.GitHubAdapter)
	}
}
