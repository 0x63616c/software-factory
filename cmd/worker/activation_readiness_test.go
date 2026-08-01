package main

import (
	"context"
	"testing"
)

type fakeLegacyExecutions struct{ count int }

func (f fakeLegacyExecutions) RunningPreActivationExecutions(context.Context) (int, error) {
	return f.count, nil
}

type fakeLegacyTickets struct{ count int64 }

func (f fakeLegacyTickets) LegacyTicketCount(context.Context) (int64, error) {
	return f.count, nil
}

func TestActivationReadinessAcceptsOnlyAQuiescentLegacyBoundary(t *testing.T) {
	t.Parallel()
	if err := ensureActivationReady(context.Background(), fakeLegacyExecutions{}, fakeLegacyTickets{}); err != nil {
		t.Fatalf("ensureActivationReady: %v", err)
	}
}

func TestActivationReadinessRejectsRunningLegacyWorkflow(t *testing.T) {
	t.Parallel()
	err := ensureActivationReady(context.Background(), fakeLegacyExecutions{count: 1}, fakeLegacyTickets{})
	if err == nil {
		t.Fatal("ensureActivationReady accepted a running legacy workflow")
	}
}

func TestActivationReadinessRejectsLegacyTicketStates(t *testing.T) {
	t.Parallel()
	err := ensureActivationReady(context.Background(), fakeLegacyExecutions{}, fakeLegacyTickets{count: 1})
	if err == nil {
		t.Fatal("ensureActivationReady accepted a pre-activation Ticket state")
	}
}
