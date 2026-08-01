package main

import (
	"context"
	"fmt"
)

type legacyExecutionLister interface {
	RunningPreActivationExecutions(context.Context) (int, error)
}

type legacyTicketLister interface {
	LegacyTicketCount(context.Context) (int64, error)
}

// ensureActivationReady is the code-side half of the operational cutover
// gate. It never mutates old work: activation simply refuses to publish an
// unpaused target policy while a legacy workflow or Ticket state remains.
func ensureActivationReady(ctx context.Context, executions legacyExecutionLister, tickets legacyTicketLister) error {
	legacyExecutions, err := executions.RunningPreActivationExecutions(ctx)
	if err != nil {
		return fmt.Errorf("checking legacy workflow executions before activation: %w", err)
	}
	if legacyExecutions != 0 {
		return fmt.Errorf("activation requires zero running pre-activation workflows; found %d", legacyExecutions)
	}
	legacyTickets, err := tickets.LegacyTicketCount(ctx)
	if err != nil {
		return fmt.Errorf("checking pre-activation Ticket states before activation: %w", err)
	}
	if legacyTickets != 0 {
		return fmt.Errorf("activation requires zero pre-activation Ticket states; found %d", legacyTickets)
	}
	return nil
}
