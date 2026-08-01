package storefake

import (
	"context"

	"github.com/0x63616c/software-factory/internal/store"
)

// RecordWebhookDelivery mirrors the target handler's delivery-only boundary.
func (f *Store) RecordWebhookDelivery(_ context.Context, deliveryID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.webhookDeliveries == nil {
		f.webhookDeliveries = make(map[string]bool)
	}
	if f.webhookDeliveries[deliveryID] {
		return false, nil
	}
	f.webhookDeliveries[deliveryID] = true
	return true, nil
}

// RecordWebhookDeliveryAndTransition mirrors the real store's single
// transaction in memory: recording deliveryID and applying the Ticket
// transition happen under the same lock, so a test sees the same
// all-or-nothing behaviour the real Postgres transaction gives.
func (f *Store) RecordWebhookDeliveryAndTransition(_ context.Context, deliveryID string, id store.TicketID, from, to store.TicketState) (store.WebhookDeliveryOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.webhookDeliveries == nil {
		f.webhookDeliveries = make(map[string]bool)
	}
	if f.webhookDeliveries[deliveryID] {
		return store.WebhookDeliveryDuplicate, nil
	}
	f.webhookDeliveries[deliveryID] = true

	ticket, ok := f.tickets[id]
	if !ok || ticket.State != from {
		return store.WebhookDeliveryStale, nil
	}
	if ticket.State == store.TicketActive || (ticket.State == store.TicketDone && to != store.TicketDone) || to == store.TicketActive {
		return store.WebhookDeliveryStale, nil
	}
	ticket.State = to
	ticket.UpdatedAt = f.clk.Now()
	f.tickets[id] = ticket
	return store.WebhookDeliveryApplied, nil
}
