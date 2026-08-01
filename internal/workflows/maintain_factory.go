package workflows

import (
	"fmt"
	"sort"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// maintainActs names the Store-backed recovery activities. Registration stays
// inactive until the PR 8 cutover; the workflow is intentionally safe to
// register with a Temporal Schedule later without changing its behavior.
var maintainActs *activities.TargetMaintenanceActivities

// targetExecutionActs is the target-only Temporal visibility activity set.
var targetExecutionActs *activities.TargetExecutionActivities

// MaintainFactory performs one bounded recovery pass. Its eventual Temporal
// Schedule supplies recurrence; keeping this workflow finite means a failed
// pass is naturally retried by the Schedule rather than holding live state.
func MaintainFactory(ctx workflow.Context) error {
	control := workflow.WithActivityOptions(ctx, maintenanceActivityOptions())
	var owners []store.ActiveTargetRunOwner
	if err := workflow.ExecuteActivity(control, maintainActs.ListActiveTargetRunOwners).Get(control, &owners); err != nil {
		return fmt.Errorf("listing active target run owners: %w", err)
	}
	var workers []work.RunWorkerIdentity
	if err := workflow.ExecuteActivity(control, runWorkerControlActs.ListRunWorkers).Get(control, &workers); err != nil {
		return fmt.Errorf("listing Run Workers: %w", err)
	}
	byRun := runWorkersByRun(workers)
	handled := make(map[string]bool, len(owners))
	for _, owner := range owners {
		var state work.RunState
		workflowID := work.TicketWorkflowID(int64(owner.TicketID))
		if err := workflow.ExecuteActivity(control, targetExecutionActs.DescribeRun, workflowID).Get(control, &state); err != nil {
			return fmt.Errorf("describing target ticket workflow %s: %w", workflowID, err)
		}
		if state.Open && state.RunID == owner.RunID {
			handled[owner.RunID] = true
			continue
		}
		if err := deleteRunWorkers(ctx, control, owner.RunID, byRun[owner.RunID]); err != nil {
			return err
		}
		var reopened bool
		if err := workflow.ExecuteActivity(control, maintainActs.ReconcileAbandonedTargetRun, owner.RunID, owner.TicketID).Get(control, &reopened); err != nil {
			return fmt.Errorf("reconciling abandoned target run %s: %w", owner.RunID, err)
		}
		if !reopened {
			workflow.GetLogger(ctx).Info("target maintenance ownership was already replaced", "ticket_id", int64(owner.TicketID), "run_id", owner.RunID)
		}
		handled[owner.RunID] = true
	}
	for _, runID := range sortedRunIDs(byRun) {
		if handled[runID] {
			continue
		}
		var run store.Run
		if err := workflow.ExecuteActivity(control, maintainActs.LookupTargetRun, runID).Get(control, &run); err != nil {
			return fmt.Errorf("reading inventoried target run %s: %w", runID, err)
		}
		if run.TargetOutcome != "" {
			if err := deleteRunWorkers(ctx, control, runID, byRun[runID]); err != nil {
				return err
			}
			continue
		}
		workflowID := work.TicketWorkflowID(int64(run.TicketID))
		var state work.RunState
		if err := workflow.ExecuteActivity(control, targetExecutionActs.DescribeRun, workflowID).Get(control, &state); err != nil {
			return fmt.Errorf("describing inventoried target ticket workflow %s: %w", workflowID, err)
		}
		if state.Open && state.RunID == runID {
			continue
		}
		if err := deleteRunWorkers(ctx, control, runID, byRun[runID]); err != nil {
			return err
		}
		var reopened bool
		if err := workflow.ExecuteActivity(control, maintainActs.ReconcileAbandonedTargetRun, runID, run.TicketID).Get(control, &reopened); err != nil {
			return fmt.Errorf("reconciling inventoried abandoned target run %s: %w", runID, err)
		}
	}
	return nil
}

func deleteRunWorkers(ctx workflow.Context, control workflow.Context, runID string, identities []work.RunWorkerIdentity) error {
	for _, identity := range identities {
		if err := workflow.ExecuteActivity(control, runWorkerControlActs.DeleteRunWorker, activities.DeleteRunWorkerInput{Identity: identity}).Get(control, nil); err != nil {
			return fmt.Errorf("deleting orphaned Run Worker generation %d for %s: %w", identity.Generation, runID, err)
		}
	}
	return nil
}

func runWorkersByRun(identities []work.RunWorkerIdentity) map[string][]work.RunWorkerIdentity {
	byRun := make(map[string][]work.RunWorkerIdentity)
	for _, identity := range identities {
		byRun[identity.RunID] = append(byRun[identity.RunID], identity)
	}
	for runID := range byRun {
		sort.Slice(byRun[runID], func(left, right int) bool { return byRun[runID][left].Generation < byRun[runID][right].Generation })
	}
	return byRun
}

func sortedRunIDs(byRun map[string][]work.RunWorkerIdentity) []string {
	runIDs := make([]string, 0, len(byRun))
	for runID := range byRun {
		runIDs = append(runIDs, runID)
	}
	sort.Strings(runIDs)
	return runIDs
}

func maintenanceActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
}
