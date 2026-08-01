package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/jackc/pgx/v5"
)

// TicketCreator files a new Ticket. The API ticket that accepts a create
// request is this method's caller.
type TicketCreator interface {
	CreateTicket(ctx context.Context, title, body string, blockedBy []TicketID) (Ticket, error)
}

// TicketReader reads one Ticket, or every Ticket in a state.
type TicketReader interface {
	Ticket(ctx context.Context, id TicketID) (Ticket, error)
	Tickets(ctx context.Context) ([]Ticket, error)
	TicketsByState(ctx context.Context, state TicketState) ([]Ticket, error)
}

// Tickets lists every Ticket, ordered by id.
func (s *Store) Tickets(ctx context.Context) ([]Ticket, error) {
	rows, err := s.q.Tickets(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tickets: %w", wrapQueryErr(err))
	}
	return ticketsFromRows(rows)
}

// TicketStateWriter moves a Ticket to a new state.
type TicketStateWriter interface {
	UpdateTicketState(ctx context.Context, id TicketID, state TicketState) (Ticket, error)
	TransitionTicketState(ctx context.Context, id TicketID, from, to TicketState) (Ticket, error)
}

// LegacyTicketReopener performs the cutover-only optimistic repair from the
// two legacy nonterminal states. The complete expected snapshots prevent a
// concurrent state write from being mistaken for cutover-owned work.
type LegacyTicketReopener interface {
	ReopenLegacyTickets(ctx context.Context, expected []Ticket) ([]Ticket, error)
}

// LegacyCutoverReconciler inventories and atomically closes the database side
// of legacy work before reopening its Tickets.
type LegacyCutoverReconciler interface {
	OpenLegacyRuns(context.Context) ([]Run, error)
	ReconcileLegacyState(context.Context, []Ticket, []Run, time.Time) ([]Ticket, error)
}

// ReadyTicketLister lists the Tickets the dispatcher may start next: open,
// with every dependency done. The dispatcher is this method's one caller.
type ReadyTicketLister interface {
	ReadyTickets(ctx context.Context) ([]Ticket, error)
}

// CreateTicket files a new Ticket in TicketOpen with all of its declared
// blockers. The ticket and its edges commit together so a ready-ticket query
// can never observe a declared blockerless Ticket.
func (s *Store) CreateTicket(ctx context.Context, title, body string, blockers []TicketID) (Ticket, error) {
	if s.begin == nil {
		return Ticket{}, fmt.Errorf("creating ticket %q: store cannot begin a transaction", title)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return Ticket{}, fmt.Errorf("creating ticket %q: beginning transaction: %w", title, wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)
	row, err := q.CreateTicket(ctx, storedb.CreateTicketParams{
		Title: title,
		Body:  body,
		State: TicketOpen.String(),
	})
	if err != nil {
		return Ticket{}, fmt.Errorf("creating ticket %q: %w", title, wrapQueryErr(err))
	}
	ticket, err := ticketFromRow(row)
	if err != nil {
		return Ticket{}, fmt.Errorf("creating ticket %q: parsing stored ticket: %w", title, err)
	}
	for _, blocker := range blockers {
		encoded, edgeErr := q.AddTicketDependencyIfAcyclic(ctx, storedb.AddTicketDependencyIfAcyclicParams{Column1: int64(ticket.ID), Column2: int64(blocker)})
		if edgeErr != nil {
			return Ticket{}, fmt.Errorf("creating ticket %d with blocker %d: %w", ticket.ID, blocker, wrapQueryErr(edgeErr))
		}
		if encoded != "" {
			return Ticket{}, fmt.Errorf("creating ticket %d with blocker %d: dependency would create cycle %s", ticket.ID, blocker, encoded)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Ticket{}, fmt.Errorf("creating ticket %d: committing transaction: %w", ticket.ID, wrapQueryErr(err))
	}
	return ticket, nil
}

// Ticket reads one Ticket by id.
func (s *Store) Ticket(ctx context.Context, id TicketID) (Ticket, error) {
	row, err := s.q.Ticket(ctx, int64(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, fmt.Errorf("reading ticket %d: %w", id, ErrNotFound)
		}
		return Ticket{}, fmt.Errorf("reading ticket %d: %w", id, wrapQueryErr(err))
	}
	return ticketFromRow(row)
}

// TicketsByState lists every Ticket in state, ordered by id.
func (s *Store) TicketsByState(ctx context.Context, state TicketState) ([]Ticket, error) {
	rows, err := s.q.TicketsByState(ctx, state.String())
	if err != nil {
		return nil, fmt.Errorf("listing %s tickets: %w", state, wrapQueryErr(err))
	}
	return ticketsFromRows(rows)
}

// ReopenLegacyTickets atomically reopens exactly expected. A changed state or
// updated_at version aborts the whole transaction, leaving every row untouched.
func (s *Store) ReopenLegacyTickets(ctx context.Context, expected []Ticket) ([]Ticket, error) {
	if len(expected) == 0 {
		return []Ticket{}, nil
	}
	if s.begin == nil {
		return nil, fmt.Errorf("reopening legacy tickets: store cannot begin a transaction")
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("reopening legacy tickets: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)
	reopened := make([]Ticket, 0, len(expected))
	for _, snapshot := range expected {
		if snapshot.State != TicketWorking && snapshot.State != TicketReview {
			return nil, fmt.Errorf("reopening legacy ticket %d: expected state %s is not legacy nonterminal", snapshot.ID, snapshot.State)
		}
		row, queryErr := q.ReopenLegacyTicket(ctx, storedb.ReopenLegacyTicketParams{
			ID:        int64(snapshot.ID),
			State:     snapshot.State.String(),
			UpdatedAt: pgTimestamp(snapshot.UpdatedAt),
		})
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("reopening legacy ticket %d: state/version changed after inventory", snapshot.ID)
		}
		if queryErr != nil {
			return nil, fmt.Errorf("reopening legacy ticket %d: %w", snapshot.ID, wrapQueryErr(queryErr))
		}
		ticket, parseErr := ticketFromRow(row)
		if parseErr != nil {
			return nil, fmt.Errorf("reopening legacy ticket %d: parsing updated row: %w", snapshot.ID, parseErr)
		}
		reopened = append(reopened, ticket)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("reopening legacy tickets: committing transaction: %w", wrapQueryErr(err))
	}
	return reopened, nil
}

// ReconcileLegacyState atomically records every still-open legacy Run as a
// preserved failed historical Run and reopens the exact Ticket snapshots.
func (s *Store) ReconcileLegacyState(ctx context.Context, expectedTickets []Ticket, expectedRuns []Run, endedAt time.Time) ([]Ticket, error) {
	if len(expectedTickets) == 0 && len(expectedRuns) == 0 {
		return []Ticket{}, nil
	}
	if s.begin == nil {
		return nil, fmt.Errorf("reconciling legacy state: store cannot begin a transaction")
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciling legacy state: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)
	for _, snapshot := range expectedRuns {
		id, parseErr := pgUUID(snapshot.ID)
		if parseErr != nil {
			return nil, fmt.Errorf("closing legacy run %s: %w", snapshot.ID, parseErr)
		}
		row, queryErr := q.CloseLegacyRun(ctx, storedb.CloseLegacyRunParams{ID: id, EndedAt: pgTimestamp(endedAt)})
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("closing legacy run %s: terminal state changed after inventory", snapshot.ID)
		}
		if queryErr != nil {
			return nil, fmt.Errorf("closing legacy run %s: %w", snapshot.ID, wrapQueryErr(queryErr))
		}
		closed := runFromRow(row)
		if closed.TicketID != snapshot.TicketID || !closed.StartedAt.Equal(snapshot.StartedAt) {
			return nil, fmt.Errorf("closing legacy run %s: snapshot changed after inventory", snapshot.ID)
		}
	}

	reopened := make([]Ticket, 0, len(expectedTickets))
	for _, snapshot := range expectedTickets {
		if snapshot.State != TicketWorking && snapshot.State != TicketReview {
			return nil, fmt.Errorf("reopening legacy ticket %d: expected state %s is not legacy nonterminal", snapshot.ID, snapshot.State)
		}
		row, queryErr := q.ReopenLegacyTicket(ctx, storedb.ReopenLegacyTicketParams{
			ID: int64(snapshot.ID), State: snapshot.State.String(), UpdatedAt: pgTimestamp(snapshot.UpdatedAt),
		})
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("reopening legacy ticket %d: state/version changed after inventory", snapshot.ID)
		}
		if queryErr != nil {
			return nil, fmt.Errorf("reopening legacy ticket %d: %w", snapshot.ID, wrapQueryErr(queryErr))
		}
		ticket, parseErr := ticketFromRow(row)
		if parseErr != nil {
			return nil, fmt.Errorf("reopening legacy ticket %d: parsing updated row: %w", snapshot.ID, parseErr)
		}
		reopened = append(reopened, ticket)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("reconciling legacy state: committing transaction: %w", wrapQueryErr(err))
	}
	return reopened, nil
}

// UpdateTicketState moves ticket id to state and returns the updated row.
func (s *Store) UpdateTicketState(ctx context.Context, id TicketID, state TicketState) (Ticket, error) {
	if state == TicketActive {
		return Ticket{}, fmt.Errorf("moving ticket %d to active: %w", id, ErrActiveTicketOwnership)
	}
	row, err := s.q.UpdateTicketState(ctx, storedb.UpdateTicketStateParams{
		ID:    int64(id),
		State: state.String(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, fmt.Errorf("moving ticket %d to %s: %w", id, state, s.ticketStateWriteError(ctx, id))
		}
		return Ticket{}, fmt.Errorf("moving ticket %d to %s: %w", id, state, wrapQueryErr(err))
	}
	return ticketFromRow(row)
}

// TransitionTicketState atomically moves id only when it remains in from.
func (s *Store) TransitionTicketState(ctx context.Context, id TicketID, from, to TicketState) (Ticket, error) {
	if to == TicketActive {
		return Ticket{}, fmt.Errorf("transitioning ticket %d to active: %w", id, ErrActiveTicketOwnership)
	}
	row, err := s.q.TransitionTicketState(ctx, storedb.TransitionTicketStateParams{ID: int64(id), State: from.String(), State_2: to.String()})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, fmt.Errorf("transitioning ticket %d from %s to %s: %w", id, from, to, s.ticketStateWriteError(ctx, id))
		}
		return Ticket{}, fmt.Errorf("transitioning ticket %d from %s to %s: %w", id, from, to, wrapQueryErr(err))
	}
	return ticketFromRow(row)
}

func (s *Store) ticketStateWriteError(ctx context.Context, id TicketID) error {
	row, err := s.q.Ticket(ctx, int64(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return wrapQueryErr(err)
	}
	switch row.State {
	case TicketActive.String():
		return ErrActiveTicketOwnership
	case TicketDone.String():
		return work.ErrPermanent
	default:
		return ErrNotFound
	}
}

// ReadyTickets lists every open Ticket whose direct dependencies are all
// done — ADR-0012's ready(T) definition, and nothing more: it does not walk
// the graph transitively, because the ADR does not ask it to.
func (s *Store) ReadyTickets(ctx context.Context) ([]Ticket, error) {
	rows, err := s.q.ReadyTickets(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing ready tickets: %w", wrapQueryErr(err))
	}
	return ticketsFromRows(rows)
}

// ticketFromRow parses a stored row into a Ticket, the one place a row's
// state string becomes a typed TicketState.
func ticketFromRow(row storedb.Ticket) (Ticket, error) {
	state, err := ParseTicketState(row.State)
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket %d: stored state %q is not a known TicketState: %w", row.ID, row.State, err)
	}
	return Ticket{
		ID:          TicketID(row.ID),
		Title:       row.Title,
		Body:        row.Body,
		State:       state,
		ActiveRunID: runIDString(row.ActiveRunID),
		CreatedAt:   timeFromPg(row.CreatedAt),
		UpdatedAt:   timeFromPg(row.UpdatedAt),
	}, nil
}

func ticketsFromRows(rows []storedb.Ticket) ([]Ticket, error) {
	tickets := make([]Ticket, 0, len(rows))
	for _, row := range rows {
		t, err := ticketFromRow(row)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	return tickets, nil
}

var (
	_ TicketCreator     = (*Store)(nil)
	_ TicketReader      = (*Store)(nil)
	_ TicketStateWriter = (*Store)(nil)
	_ ReadyTicketLister = (*Store)(nil)
)
