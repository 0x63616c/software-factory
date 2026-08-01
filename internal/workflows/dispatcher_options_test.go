package workflows

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	enums "go.temporal.io/api/enums/v1"
)

func TestDispatchWaitActivityOptionsLeaveIdleRetriesOutsideWorkflowHistory(t *testing.T) {
	options := dispatchWaitActivityOptions(work.DefaultDispatcherPolicy().Run.Recording)
	if options.ScheduleToCloseTimeout != 0 {
		t.Errorf("ScheduleToCloseTimeout = %s, want no bound so Temporal owns no-work retry cadence", options.ScheduleToCloseTimeout)
	}
	if options.RetryPolicy == nil || options.RetryPolicy.MaximumAttempts != 0 {
		t.Errorf("RetryPolicy = %+v, want unlimited retrying wait", options.RetryPolicy)
	}
}

func TestDispatchChildOptionsRequestCancellationWhenTheDispatcherCloses(t *testing.T) {
	policy := work.DefaultTargetRunPolicy()
	options := dispatchChildWorkflowOptions(store.TicketID(41), policy)
	if options.WorkflowID != work.TicketWorkflowID(41) {
		t.Errorf("WorkflowID = %q, want ticket-scoped target workflow ID", options.WorkflowID)
	}
	if options.TaskQueue != work.TaskQueue {
		t.Errorf("TaskQueue = %q, want the target run queue %q", options.TaskQueue, work.TaskQueue)
	}
	if options.WorkflowExecutionTimeout != policy.HardDeadline {
		t.Errorf("WorkflowExecutionTimeout = %s, want immutable policy hard deadline %s", options.WorkflowExecutionTimeout, policy.HardDeadline)
	}
	if options.ParentClosePolicy != enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL {
		t.Errorf("ParentClosePolicy = %s, want request cancellation", options.ParentClosePolicy)
	}
}
