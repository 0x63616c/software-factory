package workflows_test

import (
	"context"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

// These nil handles name the workflow activity methods for this test package.
// Temporal resolves methods by name; no test invokes a nil receiver.
var (
	maintenanceActs          *activities.TargetMaintenanceActivities
	maintenanceRunWorkerActs *activities.RunWorkerControlActivities
	maintenanceExecutionActs *activities.TargetExecutionActivities
)

var _ = Describe("MaintainFactory", func() {
	It("reconciles closed owners and deletes stale run workers", func() {
		suite := &testsuite.WorkflowTestSuite{}
		env := suite.NewTestWorkflowEnvironment()
		owner := store.ActiveTargetRunOwner{TicketID: 7, RunID: "019fb900-0000-7000-8000-000000000007"}
		identity, err := work.NewRunWorkerIdentity(owner.RunID, 2)
		Expect(err).NotTo(HaveOccurred(), "NewRunWorkerIdentity")
		env.OnActivity(maintenanceActs.ListActiveTargetRunOwners, mock.Anything).Return([]store.ActiveTargetRunOwner{owner}, nil)
		firstGeneration, err := work.NewRunWorkerIdentity(owner.RunID, 1)
		Expect(err).NotTo(HaveOccurred(), "NewRunWorkerIdentity(first)")
		env.OnActivity(maintenanceRunWorkerActs.ListRunWorkers, mock.Anything).Return([]work.RunWorkerIdentity{firstGeneration, identity}, nil)
		env.OnActivity(maintenanceExecutionActs.DescribeRun, mock.Anything, work.TicketWorkflowID(int64(owner.TicketID))).Return(work.RunState{Open: false}, nil)
		deleted := map[work.RunWorkerIdentity]bool{}
		env.OnActivity(maintenanceRunWorkerActs.DeleteRunWorker, mock.Anything, mock.Anything).
			Return(func(_ context.Context, in activities.DeleteRunWorkerInput) error {
				deleted[in.Identity] = true
				return nil
			})
		var reconciled store.ActiveTargetRunOwner
		env.OnActivity(maintenanceActs.ReconcileAbandonedTargetRun, mock.Anything, owner.RunID, owner.TicketID).
			Return(func(_ context.Context, runID string, ticketID store.TicketID) (bool, error) {
				reconciled = store.ActiveTargetRunOwner{RunID: runID, TicketID: ticketID}
				return true, nil
			})

		env.ExecuteWorkflow(workflows.MaintainFactory)
		Expect(env.GetWorkflowError()).NotTo(HaveOccurred(), "MaintainFactory")
		Expect(deleted).To(HaveLen(2), "deleted Run Worker identities = %v, want every generation", deleted)
		Expect(deleted).To(HaveKey(firstGeneration), "first generation should be deleted")
		Expect(deleted).To(HaveKey(identity), "latest generation should be deleted")
		Expect(reconciled).To(Equal(owner), "reconciled owner")
	})

	It("does not reopen replacement runs", func() {
		suite := &testsuite.WorkflowTestSuite{}
		env := suite.NewTestWorkflowEnvironment()
		owner := store.ActiveTargetRunOwner{TicketID: 10, RunID: "019fb900-0000-7000-8000-000000000010"}
		replacement := "019fb900-0000-7000-8000-000000000011"
		identity, err := work.NewRunWorkerIdentity(owner.RunID, 2)
		Expect(err).NotTo(HaveOccurred(), "NewRunWorkerIdentity")
		env.OnActivity(maintenanceActs.ListActiveTargetRunOwners, mock.Anything).Return([]store.ActiveTargetRunOwner{owner}, nil)
		env.OnActivity(maintenanceRunWorkerActs.ListRunWorkers, mock.Anything).Return([]work.RunWorkerIdentity{identity}, nil)
		env.OnActivity(maintenanceExecutionActs.DescribeRun, mock.Anything, work.TicketWorkflowID(int64(owner.TicketID))).Return(work.RunState{Open: true, RunID: replacement}, nil)
		env.OnActivity(maintenanceRunWorkerActs.DeleteRunWorker, mock.Anything, activities.DeleteRunWorkerInput{Identity: identity}).Return(nil)
		env.OnActivity(maintenanceActs.ReconcileAbandonedTargetRun, mock.Anything, owner.RunID, owner.TicketID).Return(false, nil)

		env.ExecuteWorkflow(workflows.MaintainFactory)
		Expect(env.GetWorkflowError()).NotTo(HaveOccurred(), "MaintainFactory")
	})

	It("leaves the current live owner untouched", func() {
		suite := &testsuite.WorkflowTestSuite{}
		env := suite.NewTestWorkflowEnvironment()
		owner := store.ActiveTargetRunOwner{TicketID: 8, RunID: "019fb900-0000-7000-8000-000000000008"}
		identity, err := work.NewRunWorkerIdentity(owner.RunID, 2)
		Expect(err).NotTo(HaveOccurred(), "NewRunWorkerIdentity")
		env.OnActivity(maintenanceActs.ListActiveTargetRunOwners, mock.Anything).Return([]store.ActiveTargetRunOwner{owner}, nil)
		env.OnActivity(maintenanceRunWorkerActs.ListRunWorkers, mock.Anything).Return([]work.RunWorkerIdentity{identity}, nil)
		env.OnActivity(maintenanceExecutionActs.DescribeRun, mock.Anything, work.TicketWorkflowID(int64(owner.TicketID))).Return(work.RunState{Open: true, RunID: owner.RunID}, nil)

		env.ExecuteWorkflow(workflows.MaintainFactory)
		Expect(env.GetWorkflowError()).NotTo(HaveOccurred(), "MaintainFactory")
	})

	It("deletes terminal run workers without reopening done tickets", func() {
		suite := &testsuite.WorkflowTestSuite{}
		env := suite.NewTestWorkflowEnvironment()
		runID := "019fb900-0000-7000-8000-000000000009"
		identity, err := work.NewRunWorkerIdentity(runID, 2)
		Expect(err).NotTo(HaveOccurred(), "NewRunWorkerIdentity")
		env.OnActivity(maintenanceActs.ListActiveTargetRunOwners, mock.Anything).Return([]store.ActiveTargetRunOwner{}, nil)
		env.OnActivity(maintenanceRunWorkerActs.ListRunWorkers, mock.Anything).Return([]work.RunWorkerIdentity{identity}, nil)
		env.OnActivity(maintenanceActs.LookupTargetRun, mock.Anything, runID).
			Return(store.Run{ID: runID, TicketID: 9, TargetOutcome: work.RunOutcomeSucceeded}, nil)
		env.OnActivity(maintenanceRunWorkerActs.DeleteRunWorker, mock.Anything, activities.DeleteRunWorkerInput{Identity: identity}).Return(nil)

		env.ExecuteWorkflow(workflows.MaintainFactory)
		Expect(env.GetWorkflowError()).NotTo(HaveOccurred(), "MaintainFactory")
	})
})
