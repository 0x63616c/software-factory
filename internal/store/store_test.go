package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/database"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, _ := newTestStoreAndPool(t)
	return s
}

func newTestStoreAndPool(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	databaseURL := config.DatabaseURL()
	if databaseURL == "" {
		t.Skip(config.DatabaseURLEnv + " is not set")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL connection: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close PostgreSQL connection: %v", err)
		}
	})
	ctx := context.Background()
	if err := database.ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return store.New(pool), pool
}

func TestStoreCarriesTicketsThroughTheFinalLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	blocker, err := s.CreateTicket(ctx, "upstream", "do this first", nil)
	if err != nil {
		t.Fatalf("CreateTicket(blocker): %v", err)
	}
	blocked, err := s.CreateTicket(ctx, "downstream", "needs upstream done", []store.TicketID{blocker.ID})
	if err != nil {
		t.Fatalf("CreateTicket(blocked): %v", err)
	}
	ready, err := s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets: %v", err)
	}
	if !containsTicket(ready, blocker.ID) || containsTicket(ready, blocked.ID) {
		t.Fatalf("ReadyTickets() = %+v, want only the blocker ready", ready)
	}
	if _, err := s.UpdateTicketState(ctx, blocker.ID, store.TicketDone); err != nil {
		t.Fatalf("UpdateTicketState(blocker, done): %v", err)
	}
	ready, err = s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets after blocker done: %v", err)
	}
	if !containsTicket(ready, blocked.ID) {
		t.Fatalf("ReadyTickets() after blocker done = %+v, want downstream", ready)
	}
	byState, err := s.TicketsByState(ctx, store.TicketDone)
	if err != nil {
		t.Fatalf("TicketsByState(done): %v", err)
	}
	if !containsTicket(byState, blocker.ID) {
		t.Fatalf("TicketsByState(done) = %+v, want blocker", byState)
	}
}

func TestCreateTicketCommitsDeclaredBlockersWithTheTicket(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	upstream, err := s.CreateTicket(ctx, "upstream", "finish first", nil)
	if err != nil {
		t.Fatalf("CreateTicket(upstream): %v", err)
	}
	downstream, err := s.CreateTicket(ctx, "downstream", "wait", []store.TicketID{upstream.ID})
	if err != nil {
		t.Fatalf("CreateTicket(downstream): %v", err)
	}
	blockers, err := s.TicketBlockers(ctx, downstream.ID)
	if err != nil {
		t.Fatalf("TicketBlockers(downstream): %v", err)
	}
	if len(blockers) != 1 || blockers[0].ID != upstream.ID {
		t.Fatalf("TicketBlockers(downstream) = %+v, want [%d]", blockers, upstream.ID)
	}
	before, err := s.Tickets(ctx)
	if err != nil {
		t.Fatalf("Tickets before rejected create: %v", err)
	}
	if _, err := s.CreateTicket(ctx, "invalid", "missing blocker", []store.TicketID{999999999}); err == nil {
		t.Fatal("CreateTicket with missing blocker succeeded")
	}
	after, err := s.Tickets(ctx)
	if err != nil {
		t.Fatalf("Tickets after rejected create: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("Tickets after rejected create = %+v, want no Ticket persisted", after)
	}
}

func TestAddTicketDependencyRejectsATicketBlockingItself(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "self", "b", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	err = s.AddTicketDependency(ctx, ticket.ID, ticket.ID)
	if !errors.Is(err, work.ErrPermanent) {
		t.Fatalf("AddTicketDependency(t, t) error = %v, want work.ErrPermanent", err)
	}
}

func containsTicket(tickets []store.Ticket, id store.TicketID) bool {
	for _, ticket := range tickets {
		if ticket.ID == id {
			return true
		}
	}
	return false
}

func newTestRunID(t *testing.T) string {
	t.Helper()
	return uuid.NewString()
}
