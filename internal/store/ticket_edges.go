package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/jackc/pgx/v5"
)

// TicketDependencyWriter records and removes one dependency edge.
//
// Cycle rejection is not this interface's job: ADR-0012 assigns it to the API
// ticket that creates edges, as application logic that runs before a write
// reaches here.
type TicketDependencyWriter interface {
	AddTicketDependency(ctx context.Context, blocker, blocked TicketID) error
	AddTicketDependencyIfAcyclic(ctx context.Context, blocker, blocked TicketID) ([]TicketID, error)
	RemoveTicketDependency(ctx context.Context, blocker, blocked TicketID) error
}

// AddTicketDependencyIfAcyclic atomically records blocker -> blocked unless
// it would close an existing blocked path into a cycle. A non-empty returned
// path describes the cycle and leaves the graph unchanged.
func (s *Store) AddTicketDependencyIfAcyclic(ctx context.Context, blocker, blocked TicketID) ([]TicketID, error) {
	encoded, err := s.q.AddTicketDependencyIfAcyclic(ctx, storedb.AddTicketDependencyIfAcyclicParams{Column1: int64(blocked), Column2: int64(blocker)})
	if err != nil {
		return nil, fmt.Errorf("adding dependency atomically: ticket %d blocks ticket %d: %w", blocker, blocked, wrapQueryErr(err))
	}
	if encoded == "" {
		return nil, nil
	}
	return ticketDependencyPathFromString(encoded)
}

// TicketDependencyReader reads one ticket's blockers and what it blocks — the
// two directions ADR-0012's one `blocks`/`blocked_by` relation is read in.
type TicketDependencyReader interface {
	TicketBlockers(ctx context.Context, ticket TicketID) ([]Ticket, error)
	TicketBlocks(ctx context.Context, ticket TicketID) ([]Ticket, error)
	TicketDependencyPath(ctx context.Context, from, to TicketID) ([]TicketID, error)
}

// TicketDependencyPath returns an existing blocker-to-blocked path, or nil
// when none exists. Its one caller uses it to reject an edge that would close
// that path into a cycle.
func (s *Store) TicketDependencyPath(ctx context.Context, from, to TicketID) ([]TicketID, error) {
	encoded, err := s.q.TicketDependencyPath(ctx, storedb.TicketDependencyPathParams{Column1: int64(from), Column2: int64(to)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("finding dependency path from ticket %d to %d: %w", from, to, wrapQueryErr(err))
	}
	return ticketDependencyPathFromString(encoded)
}

func ticketDependencyPathFromString(encoded string) ([]TicketID, error) {
	parts := strings.Split(encoded, ",")
	path := make([]TicketID, 0, len(parts))
	for _, part := range parts {
		id, parseErr := strconv.ParseInt(part, 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing stored dependency path %q: %w", encoded, parseErr)
		}
		path = append(path, TicketID(id))
	}
	return path, nil
}

// AddTicketDependency records that blocker must be done before blocked is
// ready.
func (s *Store) AddTicketDependency(ctx context.Context, blocker, blocked TicketID) error {
	err := s.q.AddTicketDependency(ctx, storedb.AddTicketDependencyParams{
		BlockerTicketID: int64(blocker),
		BlockedTicketID: int64(blocked),
	})
	if err != nil {
		return fmt.Errorf("adding dependency: ticket %d blocks ticket %d: %w", blocker, blocked, wrapQueryErr(err))
	}
	return nil
}

// RemoveTicketDependency removes a previously recorded dependency edge.
func (s *Store) RemoveTicketDependency(ctx context.Context, blocker, blocked TicketID) error {
	err := s.q.RemoveTicketDependency(ctx, storedb.RemoveTicketDependencyParams{
		BlockerTicketID: int64(blocker),
		BlockedTicketID: int64(blocked),
	})
	if err != nil {
		return fmt.Errorf("removing dependency: ticket %d blocks ticket %d: %w", blocker, blocked, wrapQueryErr(err))
	}
	return nil
}

// TicketBlockers lists every ticket that blocks ticket.
func (s *Store) TicketBlockers(ctx context.Context, ticket TicketID) ([]Ticket, error) {
	rows, err := s.q.TicketBlockers(ctx, int64(ticket))
	if err != nil {
		return nil, fmt.Errorf("reading blockers of ticket %d: %w", ticket, wrapQueryErr(err))
	}
	return ticketsFromRows(rows)
}

// TicketBlocks lists every ticket that ticket blocks.
func (s *Store) TicketBlocks(ctx context.Context, ticket TicketID) ([]Ticket, error) {
	rows, err := s.q.TicketBlocks(ctx, int64(ticket))
	if err != nil {
		return nil, fmt.Errorf("reading tickets blocked by ticket %d: %w", ticket, wrapQueryErr(err))
	}
	return ticketsFromRows(rows)
}

var (
	_ TicketDependencyWriter = (*Store)(nil)
	_ TicketDependencyReader = (*Store)(nil)
)
