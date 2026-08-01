package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type migrationFixture struct {
	provider *goose.Provider
	pool     *pgxpool.Pool
}

func newMigrationFixture(t *testing.T, version int64) migrationFixture {
	t.Helper()
	databaseURL := config.DatabaseURL()
	if databaseURL == "" {
		t.Skip(config.DatabaseURLEnv + " is not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL administration connection: %v", err)
	}
	schema := "pr8_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated migration schema: %v", err)
	}
	connectionConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL connection: %v", err)
	}
	connectionConfig.RuntimeParams["search_path"] = schema
	registered := stdlib.RegisterConnConfig(connectionConfig)
	db, err := sql.Open("pgx", registered)
	if err != nil {
		t.Fatalf("open isolated migration connection: %v", err)
	}
	migrationFiles, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatalf("open embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationFiles)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, version); err != nil {
		t.Fatalf("migrate isolated schema to %d: %v", version, err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL pool configuration: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("open isolated PostgreSQL pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if err := db.Close(); err != nil {
			t.Errorf("close isolated migration connection: %v", err)
		}
		stdlib.UnregisterConnConfig(registered)
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop isolated migration schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL administration connection: %v", err)
		}
	})
	return migrationFixture{provider: provider, pool: pool}
}

func TestTargetOnlyMigrationRejectsUnreconciledLegacyStateAtomically(t *testing.T) {
	fixture := newMigrationFixture(t, 10)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, "INSERT INTO ticket (title, body, state) VALUES ('legacy', '', 'working')"); err != nil {
		t.Fatalf("insert legacy Ticket: %v", err)
	}
	if _, err := fixture.provider.UpTo(ctx, 11); err == nil || !strings.Contains(err.Error(), "every legacy Ticket") {
		t.Fatalf("migration error = %v, want legacy Ticket gate", err)
	}
	version, err := fixture.provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != 10 {
		t.Fatalf("migration version = %d, want atomic rollback to 10", version)
	}
	var targetTableExists bool
	if err := fixture.pool.QueryRow(ctx, "SELECT to_regclass('run_agent_attempt') IS NOT NULL").Scan(&targetTableExists); err != nil || !targetTableExists {
		t.Fatalf("pre-cutover target table = %v, %v", targetTableExists, err)
	}
	var executionColumnExists bool
	if err := fixture.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'run_agent_attempt' AND column_name = 'execution_id')").Scan(&executionColumnExists); err != nil {
		t.Fatalf("inspect execution identity column: %v", err)
	}
	if executionColumnExists {
		t.Fatal("execution_id exists after rejected migration")
	}
}

func TestTargetOnlyMigrationBackfillsLegacyHistoryAndPreservesOpaqueIdentity(t *testing.T) {
	fixture := newMigrationFixture(t, 10)
	ctx := context.Background()
	legacyTicketID := insertMigrationTicket(t, fixture.pool, "legacy", "open")
	legacyRunID := uuid.New()
	startedAt := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(time.Hour)
	if _, err := fixture.pool.Exec(ctx, "INSERT INTO run (id, ticket_id, started_at, ended_at, outcome, failure_kind) VALUES ($1, $2, $3, $4, 'failed', 'other')", legacyRunID, legacyTicketID, startedAt, endedAt); err != nil {
		t.Fatalf("insert legacy Run: %v", err)
	}
	steps := []struct {
		stage     string
		turn      int
		createdAt time.Time
	}{{"review", 2, startedAt.Add(2 * time.Minute)}, {"implement", 1, startedAt.Add(time.Minute)}, {"plan", 1, startedAt.Add(time.Minute)}}
	for _, step := range steps {
		if _, err := fixture.pool.Exec(ctx, "INSERT INTO step (run_id, stage, turn, created_at) VALUES ($1, $2, $3, $4)", legacyRunID, step.stage, step.turn, step.createdAt); err != nil {
			t.Fatalf("insert legacy %s Step: %v", step.stage, err)
		}
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO attempt (
		run_id, stage, turn, attempt_no, model, effort,
		input_tokens, cached_input_tokens, output_tokens, reasoning_tokens,
		measured, started_at, ended_at, result
	) VALUES ($1, 'plan', 1, 1, 'model-a', 'medium', 11, 2, 5, 1, TRUE, $2, $3, 'succeeded')`, legacyRunID, startedAt.Add(time.Minute), startedAt.Add(90*time.Second)); err != nil {
		t.Fatalf("insert legacy Attempt: %v", err)
	}
	compressed, checksum := []byte("opaque compressed bytes"), []byte("opaque checksum")
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO transcript (
		run_id, stage, turn, attempt_no, compressed_bytes, compression, uncompressed_size_bytes, checksum
	) VALUES ($1, 'plan', 1, 1, $2, 'zstd', 42, $3)`, legacyRunID, compressed, checksum); err != nil {
		t.Fatalf("insert legacy transcript: %v", err)
	}

	targetTicketID := insertMigrationTicket(t, fixture.pool, "target", "open")
	targetRunID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, "INSERT INTO run (id, ticket_id, started_at) VALUES ($1, $2, $3)", targetRunID, targetTicketID, startedAt); err != nil {
		t.Fatalf("insert target Run: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, "UPDATE ticket SET state = 'active', active_run_id = $1 WHERE id = $2", targetRunID, targetTicketID); err != nil {
		t.Fatalf("activate target Ticket: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, "INSERT INTO run_step (run_id, ordinal, kind, iteration, state, started_at) VALUES ($1, 1, 'plan', 1, 'running', $2)", targetRunID, startedAt); err != nil {
		t.Fatalf("insert target Step: %v", err)
	}
	const opaqueIdentity = "opaque://execution/not-a-provider-thread"
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO run_agent_attempt (
		run_id, step_ordinal, attempt_no, agent_stage, model, effort, state,
		provider_thread_id, usage_state, started_at
	) VALUES ($1, 1, 1, 'plan', 'model-b', 'high', 'running', $2, 'unknown', $3)`, targetRunID, opaqueIdentity, startedAt); err != nil {
		t.Fatalf("insert target Agent Attempt: %v", err)
	}

	if _, err := fixture.provider.UpTo(ctx, 11); err != nil {
		t.Fatalf("apply target-only migration: %v", err)
	}
	rows, err := fixture.pool.Query(ctx, "SELECT ordinal, kind, iteration FROM run_step WHERE run_id = $1 ORDER BY ordinal", legacyRunID)
	if err != nil {
		t.Fatalf("read backfilled Steps: %v", err)
	}
	defer rows.Close()
	var ordered []string
	for rows.Next() {
		var ordinal, iteration int
		var kind string
		if err := rows.Scan(&ordinal, &kind, &iteration); err != nil {
			t.Fatalf("scan backfilled Step: %v", err)
		}
		ordered = append(ordered, fmt.Sprintf("%d:%s:%d", ordinal, kind, iteration))
	}
	if got, want := strings.Join(ordered, ","), "1:plan:1,2:implement:1,3:review:2"; got != want {
		t.Fatalf("backfilled Step order = %q, want %q", got, want)
	}
	var inputTokens int64
	if err := fixture.pool.QueryRow(ctx, "SELECT input_tokens FROM run_agent_attempt WHERE run_id = $1 AND step_ordinal = 1 AND attempt_no = 1", legacyRunID).Scan(&inputTokens); err != nil || inputTokens != 11 {
		t.Fatalf("backfilled Attempt input tokens = %d, %v; want 11", inputTokens, err)
	}
	var gotCompressed, gotChecksum []byte
	if err := fixture.pool.QueryRow(ctx, "SELECT compressed_bytes, checksum FROM run_agent_transcript WHERE run_id = $1 AND step_ordinal = 1 AND attempt_no = 1", legacyRunID).Scan(&gotCompressed, &gotChecksum); err != nil || string(gotCompressed) != string(compressed) || string(gotChecksum) != string(checksum) {
		t.Fatalf("backfilled transcript = %q/%q, %v", gotCompressed, gotChecksum, err)
	}
	var gotIdentity string
	if err := fixture.pool.QueryRow(ctx, "SELECT execution_id FROM run_agent_attempt WHERE run_id = $1", targetRunID).Scan(&gotIdentity); err != nil || gotIdentity != opaqueIdentity {
		t.Fatalf("preserved execution identity = %q, %v; want %q", gotIdentity, err, opaqueIdentity)
	}
	for _, table := range []string{"step", "attempt", "transcript"} {
		var count int
		if err := fixture.pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retired %s rows = %d, %v; want zero", table, count, err)
		}
	}
	if _, err := fixture.pool.Exec(ctx, "INSERT INTO ticket (title, body, state) VALUES ('invalid', '', 'working')"); err == nil {
		t.Fatal("final Ticket constraint accepted working")
	}
	if results, err := fixture.provider.UpTo(ctx, 11); err != nil || len(results) != 0 {
		t.Fatalf("reapplying migration = %d results, %v; want no-op", len(results), err)
	}
}

func insertMigrationTicket(t *testing.T, pool *pgxpool.Pool, title, state string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), "INSERT INTO ticket (title, body, state) VALUES ($1, '', $2) RETURNING id", title, state).Scan(&id); err != nil {
		t.Fatalf("insert %s Ticket: %v", title, err)
	}
	return id
}

func TestApplyMigrationsCreatesProbeTable(t *testing.T) {
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
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)

	exists, err := store.New(pool).MigrationProbeExists(ctx)
	if err != nil {
		t.Fatalf("query migration probe table: %v", err)
	}
	if !exists {
		t.Error("migration probe table does not exist")
	}
}

func TestTicketActiveStateRequiresRunOwnership(t *testing.T) {
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
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, state := range []string{"working", "review"} {
		if _, err := pool.Exec(ctx, "INSERT INTO ticket (title, body, state) VALUES ($1, '', $2)", "legacy "+state, state); err == nil {
			t.Fatalf("insert legacy %s ticket succeeded after target-only migration", state)
		}
	}
	if _, err := pool.Exec(ctx, "INSERT INTO ticket (title, body, state) VALUES ('missing owner', '', 'active')"); err == nil {
		t.Fatal("insert active ticket without run ownership succeeded")
	}

	var ticketID int64
	if err := pool.QueryRow(ctx, "INSERT INTO ticket (title, body, state) VALUES ('ownership', '', 'open') RETURNING id").Scan(&ticketID); err != nil {
		t.Fatalf("insert open ticket: %v", err)
	}
	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO run (id, ticket_id, started_at) VALUES ($1, $2, CURRENT_TIMESTAMP)", runID, ticketID); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE ticket SET active_run_id = $1 WHERE id = $2", runID, ticketID); err == nil {
		t.Fatal("add active run owner without active state succeeded")
	}
	if _, err := pool.Exec(ctx, "UPDATE ticket SET state = 'active', active_run_id = $1 WHERE id = $2", runID, ticketID); err != nil {
		t.Fatalf("activate ticket with run ownership: %v", err)
	}
}

func TestTicketActiveRunMustBelongToTheSameTicket(t *testing.T) {
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
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)

	var firstTicketID, secondTicketID int64
	if err := pool.QueryRow(ctx, "INSERT INTO ticket (title, body, state) VALUES ('first ownership', '', 'open') RETURNING id").Scan(&firstTicketID); err != nil {
		t.Fatalf("insert first ticket: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO ticket (title, body, state) VALUES ('second ownership', '', 'open') RETURNING id").Scan(&secondTicketID); err != nil {
		t.Fatalf("insert second ticket: %v", err)
	}
	secondRunID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO run (id, ticket_id, started_at) VALUES ($1, $2, CURRENT_TIMESTAMP)", secondRunID, secondTicketID); err != nil {
		t.Fatalf("insert second ticket Run: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE ticket SET state = 'active', active_run_id = $1 WHERE id = $2", secondRunID, firstTicketID); err == nil {
		t.Fatal("activated Ticket with another Ticket's Run")
	}

	s := store.New(pool)
	claimTicket, err := s.CreateTicket(ctx, "claim still works", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(claim): %v", err)
	}
	claimRunID := uuid.NewString()
	claimed, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: claimTicket.ID, RunID: claimRunID, StartedAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if claimed.Ticket.State != store.TicketActive || claimed.Ticket.ActiveRunID != claimRunID {
		t.Fatalf("claimed Ticket = %+v, want active owner %s", claimed.Ticket, claimRunID)
	}
}
