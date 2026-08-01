package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/jackc/pgx/v5"
)

// WebhookDeliveryOutcome is what happened to one webhook delivery's attempt
// to move a Ticket — the three-way answer #557's acceptance criteria need,
// none of which is an error.
type WebhookDeliveryOutcome int

const (
	// WebhookDeliveryApplied means this delivery id had not been seen before,
	// and the named Ticket transition applied. A same-state transition is an
	// applied idempotent write for a new delivery, including done to done.
	WebhookDeliveryApplied WebhookDeliveryOutcome = iota
	// WebhookDeliveryDuplicate means this delivery id was already recorded —
	// GitHub's "Redeliver" button or the relay's own retry, and nothing here
	// was repeated.
	WebhookDeliveryDuplicate
	// WebhookDeliveryStale means this delivery id was new, but the Ticket was
	// no longer in the expected `from` state (a human already moved it, or an
	// earlier, unrelated delivery already did). The delivery is still
	// recorded seen; nothing else changes.
	WebhookDeliveryStale
)

// WebhookDeliveryRecorder is the narrow door the factory's GitHub webhook
// consumer needs: record a delivery id exactly once, and only the first time,
// apply one Ticket transition.
type WebhookDeliveryRecorder interface {
	RecordWebhookDeliveryAndTransition(ctx context.Context, deliveryID string, id TicketID, from, to TicketState) (WebhookDeliveryOutcome, error)
}

// WebhookDeliveryAcknowledger records a target webhook delivery exactly once
// without coupling receipt to a Ticket state transition.
type WebhookDeliveryAcknowledger interface {
	RecordWebhookDelivery(ctx context.Context, deliveryID string) (bool, error)
}

// RecordWebhookDelivery persists deliveryID once. It returns true only for
// the first receipt; a relay retry or GitHub redelivery returns false.
func (s *Store) RecordWebhookDelivery(ctx context.Context, deliveryID string) (bool, error) {
	if _, err := s.q.RecordWebhookDelivery(ctx, deliveryID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("recording webhook delivery %s: %w", deliveryID, wrapQueryErr(err))
	}
	return true, nil
}

// RecordWebhookDeliveryAndTransition records deliveryID as seen and, only the
// first time it is seen, attempts to move ticket id from `from` to `to` — in
// one Postgres transaction, so "this delivery is durably recorded" and "the
// Ticket moved" commit or fail together. That is what lets the HTTP consumer
// respond to GitHub only after this returns: acknowledging the delivery and
// durably having acted on it become the same fact, so there is no window
// after acknowledgement in which the effect could still be lost.
// A same-state transition counts as applied: it is the idempotent effect named
// by a new delivery, while a different transition out of done remains stale.
//
// A Store that cannot begin its own transaction (built over a bare pgx.Tx, or
// a fake with no beginner) cannot serve this method; see New's doc comment.
func (s *Store) RecordWebhookDeliveryAndTransition(ctx context.Context, deliveryID string, id TicketID, from, to TicketState) (WebhookDeliveryOutcome, error) {
	if s.begin == nil {
		return 0, fmt.Errorf("recording webhook delivery %s: store cannot begin a transaction", deliveryID)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("recording webhook delivery %s: beginning transaction: %w", deliveryID, wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)
	if _, err := q.RecordWebhookDelivery(ctx, deliveryID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already recorded by an earlier delivery of the same id: nothing
			// to commit, nothing to apply. The deferred Rollback is a no-op
			// cleanup of a transaction that never wrote anything.
			return WebhookDeliveryDuplicate, nil
		}
		return 0, fmt.Errorf("recording webhook delivery %s: %w", deliveryID, wrapQueryErr(err))
	}

	outcome := WebhookDeliveryApplied
	if _, err := q.TransitionTicketState(ctx, storedb.TransitionTicketStateParams{
		ID: int64(id), State: from.String(), State_2: to.String(),
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("recording webhook delivery %s: transitioning ticket %d from %s to %s: %w", deliveryID, id, from, to, wrapQueryErr(err))
		}
		// The ticket was not in `from` — already moved on by a human or an
		// earlier delivery. The delivery itself is still real and still gets
		// recorded seen; only the transition is skipped.
		outcome = WebhookDeliveryStale
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("recording webhook delivery %s: committing transaction: %w", deliveryID, wrapQueryErr(err))
	}
	return outcome, nil
}

var (
	_ WebhookDeliveryRecorder     = (*Store)(nil)
	_ WebhookDeliveryAcknowledger = (*Store)(nil)
)
