package workflows_test

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/workflows"
	"go.temporal.io/sdk/worker"
)

// TestWorkOnTicketReplaysExportedHistory protects the target workflow's
// command sequence with the same JSON format emitted by `temporal workflow
// show`. The fixture records the representative happy path through confirmed
// merge, durable terminal recording, and worker-session cleanup.
func TestWorkOnTicketReplaysExportedHistory(t *testing.T) {
	t.Parallel()
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(workflows.WorkOnTicket)
	if err := replayer.ReplayWorkflowHistoryFromJSONFile(nil, "testdata/work-on-ticket-history.json"); err != nil {
		t.Fatalf("replay WorkOnTicket history: %v", err)
	}
}
