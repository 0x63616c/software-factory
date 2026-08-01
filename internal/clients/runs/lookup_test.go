package runs

import (
	"context"
	"errors"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/0x63616c/software-factory/internal/work"
)

// fakeDescriber lets a test drive Lookup.Describe without a Temporal
// connection: it answers with whatever response or error the test plants,
// exactly like the *client.Client method it replaces.
type fakeDescriber struct {
	resp *workflowservice.DescribeWorkflowExecutionResponse
	err  error

	gotWorkflowID, gotRunID string
}

func (f *fakeDescriber) DescribeWorkflowExecution(
	_ context.Context, workflowID, runID string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	f.gotWorkflowID, f.gotRunID = workflowID, runID
	return f.resp, f.err
}

func TestDescribeAsksForTheLatestRunUnderTheWorkflowID(t *testing.T) {
	t.Parallel()

	fake := &fakeDescriber{resp: &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflow.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{WorkflowId: "work-ticket-1", RunId: "run-1"},
		},
	}}
	l := &Lookup{client: fake}

	if _, err := l.Describe(context.Background(), "work-ticket-1"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if fake.gotWorkflowID != "work-ticket-1" {
		t.Errorf("workflowID = %q, want %q", fake.gotWorkflowID, "work-ticket-1")
	}
	// Empty run ID asks Temporal for the current run, which is what the
	// dispatcher needs: it tracks a ticket by workflow ID, and ContinueAsNew
	// or a retried claim both move the current run to a new run ID.
	if fake.gotRunID != "" {
		t.Errorf("runID = %q, want empty (the latest run)", fake.gotRunID)
	}
}

func TestDescribeReportsARunningWorkflowAsOpen(t *testing.T) {
	t.Parallel()

	fake := &fakeDescriber{resp: &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflow.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{WorkflowId: "work-ticket-1", RunId: "run-1"},
		},
	}}
	l := &Lookup{client: fake}

	got, err := l.Describe(context.Background(), "work-ticket-1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	want := work.RunState{Open: true, RunID: "run-1"}
	if got != want {
		t.Errorf("Describe = %+v, want %+v", got, want)
	}
}

func TestDescribeReportsAClosedWorkflowAsNotOpen(t *testing.T) {
	t.Parallel()

	for _, status := range []enums.WorkflowExecutionStatus{
		enums.WORKFLOW_EXECUTION_STATUS_COMPLETED,
		enums.WORKFLOW_EXECUTION_STATUS_FAILED,
		enums.WORKFLOW_EXECUTION_STATUS_CANCELED,
		enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
	} {
		t.Run(status.String(), func(t *testing.T) {
			t.Parallel()

			fake := &fakeDescriber{resp: &workflowservice.DescribeWorkflowExecutionResponse{
				WorkflowExecutionInfo: &workflow.WorkflowExecutionInfo{
					Status:    status,
					Execution: &commonpb.WorkflowExecution{WorkflowId: "work-ticket-1", RunId: "run-1"},
				},
			}}
			l := &Lookup{client: fake}

			got, err := l.Describe(context.Background(), "work-ticket-1")
			if err != nil {
				t.Fatalf("Describe: %v", err)
			}
			// A closed run still identifies the execution that consumed this
			// workflow ID. The dispatcher uses Open, not RunID, for capacity;
			// retaining this value lets its duplicate-ID notice name that run.
			want := work.RunState{Open: false, RunID: "run-1"}
			if got != want {
				t.Errorf("Describe = %+v, want %+v", got, want)
			}
		})
	}
}

// TestDescribeTreatsNotFoundAsClosed proves a workflow ID that never existed
// answers the same as one that closed — see work.RunState's own doc comment
// — rather than surfacing NotFound as an activity error the dispatcher's
// reconcile would then have to special-case.
func TestDescribeTreatsNotFoundAsClosed(t *testing.T) {
	t.Parallel()

	fake := &fakeDescriber{err: serviceerror.NewNotFound("workflow not found")}
	l := &Lookup{client: fake}

	got, err := l.Describe(context.Background(), "work-ticket-404")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got != (work.RunState{}) {
		t.Errorf("Describe on NotFound = %+v, want the zero value", got)
	}
}

// TestDescribeReturnsOtherFailures proves NotFound is the only error this
// method absorbs; anything else — an unreachable frontend, a permission
// error — is a real failure and must reach the caller as one.
func TestDescribeReturnsOtherFailures(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("frontend unavailable")
	fake := &fakeDescriber{err: sentinel}
	l := &Lookup{client: fake}

	_, err := l.Describe(context.Background(), "work-ticket-1")
	if err == nil {
		t.Fatal("Describe: want an error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Describe error = %v, want it to wrap %v", err, sentinel)
	}
}
