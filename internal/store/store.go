package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound reports that a requested store record does not exist.
var ErrNotFound = errors.New("store record not found")

// beginner is satisfied by *pgxpool.Pool (and by pgx.Conn), never by a bare
// pgx.Tx or a fake — it is what lets RecordWebhookDeliveryAndTransition open
// its own transaction without this package importing pgxpool, which would
// otherwise force every test double to grow a fake connection pool just to
// satisfy a method most of them never call.
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store is the factory's Postgres store. Its methods are grouped into the
// narrow interfaces declared alongside them (TicketReader, TicketWriter,
// RunRecorder, and so on) by the consumer need each one serves — a caller
// depends on the one or two it actually uses, never on Store as a whole.
//
// Construction is manual: New takes the one thing every method needs — a
// connection to query — and nothing else.
type Store struct {
	q     *storedb.Queries
	begin beginner
}

// New wraps db as a Store. db is typically a *pgxpool.Pool; storedb.DBTX is
// the interface that both it and a pgx.Tx satisfy, so a caller that needs a
// transaction can pass one instead. A Store built over a bare pgx.Tx (or
// anything else that cannot begin its own transaction) simply cannot serve
// RecordWebhookDeliveryAndTransition — see that method's own doc comment.
func New(db storedb.DBTX) *Store {
	s := &Store{q: storedb.New(db)}
	if b, ok := db.(beginner); ok {
		s.begin = b
	}
	return s
}

// MigrationProbeExists reports whether the migration probe table exists. It
// exists only so a test can confirm the embedded migrations actually ran —
// internal/database's TestApplyMigrationsCreatesProbeTable is its one caller.
func (s *Store) MigrationProbeExists(ctx context.Context) (bool, error) {
	exists, err := s.q.MigrationProbeExists(ctx)
	if err != nil {
		return false, fmt.Errorf("querying the migration probe table: %w", wrapQueryErr(err))
	}
	return exists, nil
}

// wrapQueryErr classifies a pgx error against Temporal's retry taxonomy
// (SoftwareStyle: errors map onto Temporal's taxonomy, not a parallel one). A
// PostgreSQL integrity constraint violation — bad input, never fixed by
// retrying — is wrapped in work.ErrPermanent, which internal/activities
// already translates into a non-retryable ApplicationError. Anything else,
// including a connection failure, is left retryable, which is Temporal's
// default.
func wrapQueryErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && isIntegrityConstraintViolation(pgErr.Code) {
		return fmt.Errorf("%w: %w", work.ErrPermanent, err)
	}
	return err
}

// isIntegrityConstraintViolation reports whether code is one of PostgreSQL's
// class 23 SQLSTATE codes — a CHECK, UNIQUE, NOT NULL or FOREIGN KEY
// violation. See https://www.postgresql.org/docs/current/errcodes-appendix.html.
func isIntegrityConstraintViolation(code string) bool {
	return len(code) == 5 && code[:2] == "23"
}
