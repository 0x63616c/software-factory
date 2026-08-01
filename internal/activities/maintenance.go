package activities

import (
	"context"
	"fmt"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// TargetMaintenanceStore is the narrow persistent ownership view the
// scheduled maintainer needs. Run is non-secret terminal evidence for an
// inventoried Run Worker's cleanup; it never carries worker credentials.
type TargetMaintenanceStore interface {
	TicketsByState(context.Context, store.TicketState) ([]store.Ticket, error)
	Run(context.Context, string) (store.Run, error)
	ReconcileAbandonedRun(context.Context, string, store.TicketID) (bool, error)
}

// TargetMaintenanceActivities exposes the Store side of recovery. Temporal
// execution liveness and Run Worker deletion remain independent activities so
// the workflow makes the ordering and failure policy explicit.
type TargetMaintenanceActivities struct{ store TargetMaintenanceStore }

// TargetExecutionLookup is the one Temporal visibility read maintenance needs.
// It is kept outside the retired legacy activity bundle so activation does not
// construct sandbox or remote-exec clients merely to describe a workflow.
type TargetExecutionLookup interface {
	Describe(context.Context, string) (work.RunState, error)
}

// TargetExecutionActivities exposes strongly consistent target workflow
// liveness without importing the legacy sandbox activity surface.
type TargetExecutionActivities struct{ runs TargetExecutionLookup }

// NewTargetMaintenanceActivities constructs the Store adapter for one
// maintenance workflow execution.
func NewTargetMaintenanceActivities(store TargetMaintenanceStore) (*TargetMaintenanceActivities, error) {
	if store == nil {
		return nil, fmt.Errorf("target maintenance activities: a store is required")
	}
	return &TargetMaintenanceActivities{store: store}, nil
}

// NewTargetExecutionActivities constructs the target-only liveness adapter.
func NewTargetExecutionActivities(runs TargetExecutionLookup) (*TargetExecutionActivities, error) {
	if runs == nil {
		return nil, fmt.Errorf("target execution activities: a workflow lookup is required")
	}
	return &TargetExecutionActivities{runs: runs}, nil
}

// DescribeRun reports whether the latest execution under workflowID is open.
func (a *TargetExecutionActivities) DescribeRun(ctx context.Context, workflowID string) (work.RunState, error) {
	state, err := a.runs.Describe(ctx, workflowID)
	if err != nil {
		return work.RunState{}, fail(ctx, fmt.Sprintf("describing target workflow %s", workflowID), err)
	}
	return state, nil
}

// ListActiveTargetRunOwners returns the only ownership pairs maintenance may
// repair, ordered by Ticket ID as TicketsByState guarantees.
func (a *TargetMaintenanceActivities) ListActiveTargetRunOwners(ctx context.Context) ([]store.ActiveTargetRunOwner, error) {
	tickets, err := a.store.TicketsByState(ctx, store.TicketActive)
	if err != nil {
		return nil, fail(ctx, "listing active target ticket owners", err)
	}
	owners := make([]store.ActiveTargetRunOwner, 0, len(tickets))
	for _, ticket := range tickets {
		if ticket.ActiveRunID == "" {
			return nil, fail(ctx, fmt.Sprintf("reading active ticket %d", ticket.ID), fmt.Errorf("active target ticket has no Run owner"))
		}
		owners = append(owners, store.ActiveTargetRunOwner{TicketID: ticket.ID, RunID: ticket.ActiveRunID})
	}
	return owners, nil
}

// LookupTargetRun reads durable terminal evidence for one inventoried Run
// Worker. A terminal Run can lose its teardown after its Ticket is already
// done, so it cannot be found by active-ticket ownership alone.
func (a *TargetMaintenanceActivities) LookupTargetRun(ctx context.Context, runID string) (store.Run, error) {
	run, err := a.store.Run(ctx, runID)
	if err != nil {
		return store.Run{}, fail(ctx, fmt.Sprintf("reading target run %s", runID), err)
	}
	return run, nil
}

// ReconcileAbandonedTargetRun conditionally releases the exact active owner.
// A false result is the idempotent stale-race outcome: a live finalizer or
// replacement Run changed ownership first, so maintenance leaves it alone.
func (a *TargetMaintenanceActivities) ReconcileAbandonedTargetRun(ctx context.Context, runID string, ticketID store.TicketID) (bool, error) {
	reopened, err := a.store.ReconcileAbandonedRun(ctx, runID, ticketID)
	if err != nil {
		return false, fail(ctx, fmt.Sprintf("reconciling abandoned target run %s", runID), err)
	}
	return reopened, nil
}
