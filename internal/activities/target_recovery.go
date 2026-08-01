package activities

import (
	"context"
	"fmt"

	"github.com/0x63616c/software-factory/internal/store"
)

// TargetRecoveryActivities keeps canceled-run lookup on the main-control
// worker. It passes only a durable Git checkpoint to the private Run Worker.
type TargetRecoveryActivities struct {
	store store.CanceledRunRecoveryReader
}

// CanceledRunCheckpoint is the complete non-secret result returned by the
// activity boundary.
type CanceledRunCheckpoint struct {
	Checkpoint       store.GitCheckpoint
	MergeStepOrdinal int
	Found            bool
}

// NewTargetRecoveryActivities constructs the main-control recovery boundary.
func NewTargetRecoveryActivities(reader store.CanceledRunRecoveryReader) (*TargetRecoveryActivities, error) {
	if reader == nil {
		return nil, fmt.Errorf("target recovery activities: a canceled run recovery reader is required")
	}
	return &TargetRecoveryActivities{store: reader}, nil
}

// LatestCanceledRunCheckpoint looks up a predecessor after the successor has
// atomically claimed its Ticket, so it cannot authorize an unrelated Ticket.
func (a *TargetRecoveryActivities) LatestCanceledRunCheckpoint(ctx context.Context, ticketID store.TicketID, excludingRunID string) (CanceledRunCheckpoint, error) {
	recovery, found, err := a.store.LatestCanceledRunCheckpoint(ctx, ticketID, excludingRunID)
	if err != nil {
		return CanceledRunCheckpoint{}, fail(ctx, fmt.Sprintf("reading canceled recovery checkpoint for ticket %d", ticketID), err)
	}
	return CanceledRunCheckpoint{Checkpoint: recovery.Checkpoint, MergeStepOrdinal: recovery.MergeStepOrdinal, Found: found}, nil
}
