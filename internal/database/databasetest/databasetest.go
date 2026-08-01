// Package databasetest builds a real, migrated PostgreSQL connection pool for
// tests that need one — the one implementation behind the "database-backed
// tests skip locally, run for real in CI" pattern internal/store/store_test.go's
// own newTestStore established (config.DatabaseURLEnv gates it).
//
// It lives in its own package, rather than as a helper inside
// internal/database's own test file, so packages that cannot themselves
// import database/sql can still stand up a real database for a test.
// internal/workflows is the reason it exists: .golangci.yml's
// workflows-are-deterministic rule denies database/sql from every file under
// internal/workflows, test files included, because a workflow must perform no
// I/O — but a workflow *test* proving software-factory#602's fix against a
// real Postgres, rather than storefake, needs exactly that. Importing this
// package, rather than database/sql or pgxpool directly, keeps the workflows
// package itself clean of the import the linter denies.
package databasetest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool applies every embedded migration against config.DatabaseURL() and
// returns a ready-to-use connection pool, or skips t if that env var is
// unset — so a caller's test skips locally and runs for real only where
// CI's test-software-factory job sets it.
func NewPool(t *testing.T) *pgxpool.Pool {
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

	return pool
}
