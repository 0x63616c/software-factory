// Package runs answers whether a ticket's workflow is still open.
//
// It is one call — DescribeWorkflowExecution, strongly consistent — and
// exists as its own package for the same reason internal/clients/k8s and
// internal/clients/github do: the Temporal client type this wraps is not
// this service's domain vocabulary, and activities.RunLookup names only the
// one method the dispatcher's reconcile needs.
package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/work"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
)

// describer is the one method this package uses off temporal.Client. Named
// narrowly so a test can fake it without holding a Temporal connection —
// *temporal.Client's real implementation dials the frontend, which a unit test
// has none of.
type describer interface {
	DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

// Lookup answers RunLookup against a live Temporal client.
type Lookup struct {
	client describer
}

// New wraps a Temporal client for RunLookup. The client is not this
// package's to close: it is the same connection the worker polls its task
// queue on, owned and closed by the composition root.
func New(c temporal.Client) *Lookup {
	return &Lookup{client: c}
}

// Describe reports whether workflowID's most recent run is still open.
//
// An empty run ID asks for the latest run under that workflow ID, which is
// what the dispatcher wants: it tracks a ticket by its workflow ID, not by
// any one run of it, because ContinueAsNew and a retried claim both start a
// new run under the same ID.
//
// A workflow that has never existed and a NotFound from Temporal are the same
// answer as one that has closed — see work.RunState's own doc comment — so
// both report Open: false rather than an error. Anything else Temporal
// returns is a real failure and is returned as one.
func (l *Lookup) Describe(ctx context.Context, workflowID string) (work.RunState, error) {
	resp, err := l.client.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			return work.RunState{Open: false}, nil
		}
		return work.RunState{}, fmt.Errorf("describing workflow %s: %w", workflowID, err)
	}

	info := resp.GetWorkflowExecutionInfo()
	if info == nil {
		return work.RunState{}, fmt.Errorf("describing workflow %s: Temporal returned no execution info", workflowID)
	}

	open := info.GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_RUNNING
	runID := ""
	if info.GetExecution() != nil {
		runID = info.GetExecution().GetRunId()
	}
	return work.RunState{Open: open, RunID: runID}, nil
}
