package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"go.temporal.io/sdk/temporal"
)

// ErrTypeNoDispatchableTickets identifies the expected retryable idle result.
const ErrTypeNoDispatchableTickets = "NoDispatchableTickets"

const awaitDispatchableTicketsRetryDelay = 10 * time.Second

// TicketActivities is the narrow Postgres activity set used only by the
// Ticket-backed workflows. Keeping it separate prevents the GitHub workflow
// from acquiring a dependency on factory Ticket rows.
//
// It deliberately does not depend on store.DispatcherStateWriter: that writer
// owns the single dispatcher_state row the legacy dispatcher already writes
// every tick (#551), shaped around GitHub issue numbers
// (store.DispatcherState's own doc comment). This dispatcher pointing at that
// same row is later work, not part of standing the second pipeline up — doing
// it now would mean two dispatchers racing to overwrite one row with two
// different shapes of "what am I doing".
type TicketActivities struct {
	store interface {
		store.ReadyTicketLister
		store.TicketStateWriter
	}
}

// NewTicketActivities constructs TicketActivities over the factory store.
func NewTicketActivities(s interface {
	store.ReadyTicketLister
	store.TicketStateWriter
},
) (*TicketActivities, error) {
	if s == nil {
		return nil, fmt.Errorf("ticket activities: a store is required")
	}
	return &TicketActivities{store: s}, nil
}

// ListReadyTickets lists only Tickets whose direct dependencies are done.
func (a *TicketActivities) ListReadyTickets(ctx context.Context) ([]store.Ticket, error) {
	tickets, err := a.store.ReadyTickets(ctx)
	if err != nil {
		return nil, fail(ctx, "listing ready factory tickets", err)
	}
	return tickets, nil
}

// AwaitDispatchableTickets returns a non-empty batch or an expected retryable
// wait. Temporal owns the cadence, so idle time does not add workflow timers.
func (a *TicketActivities) AwaitDispatchableTickets(ctx context.Context) ([]store.Ticket, error) {
	tickets, err := a.ListReadyTickets(ctx)
	if err != nil {
		return nil, err
	}
	if len(tickets) == 0 {
		return nil, temporal.NewApplicationErrorWithOptions(
			"no dispatchable factory tickets", ErrTypeNoDispatchableTickets,
			temporal.ApplicationErrorOptions{NextRetryDelay: awaitDispatchableTicketsRetryDelay},
		)
	}
	return tickets, nil
}

// TransitionTicketState applies an owned lifecycle transition atomically.
func (a *TicketActivities) TransitionTicketState(ctx context.Context, id store.TicketID, from, to store.TicketState) (store.Ticket, error) {
	ticket, err := a.store.TransitionTicketState(ctx, id, from, to)
	if err != nil {
		return store.Ticket{}, fail(ctx, fmt.Sprintf("moving factory ticket %d from %s to %s", id, from, to), err)
	}
	return ticket, nil
}
