package workflows

import (
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	enums "go.temporal.io/api/enums/v1"
)

var _ = Describe("dispatcher activity options", func() {
	It("leaves wait retries outside workflow history", func() {
		options := dispatchWaitActivityOptions(work.DefaultDispatcherPolicy().Run.Recording)
		Expect(options.ScheduleToCloseTimeout).To(BeZero(), "ScheduleToCloseTimeout = %s, want no bound so Temporal owns no-work retry cadence", options.ScheduleToCloseTimeout)
		Expect(options.RetryPolicy).NotTo(BeNil(), "RetryPolicy must exist")
		Expect(options.RetryPolicy.MaximumAttempts).To(Equal(int32(0)), "RetryPolicy = %+v, want unlimited retrying wait", options.RetryPolicy)
	})

	It("requests cancellation when dispatcher closes", func() {
		policy := work.DefaultTargetRunPolicy()
		options := dispatchChildWorkflowOptions(store.TicketID(41), policy)
		Expect(options.WorkflowID).To(Equal(work.TicketWorkflowID(41)), "WorkflowID = %q, want ticket-scoped target workflow ID", options.WorkflowID)
		Expect(options.TaskQueue).To(Equal(work.TaskQueue), "TaskQueue = %q, want the target run queue %q", options.TaskQueue)
		Expect(options.WorkflowExecutionTimeout).To(Equal(policy.HardDeadline), "WorkflowExecutionTimeout = %s, want immutable policy hard deadline %s", options.WorkflowExecutionTimeout, policy.HardDeadline)
		Expect(options.ParentClosePolicy).To(Equal(enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL), "ParentClosePolicy = %s, want request cancellation", options.ParentClosePolicy)
	})
})
