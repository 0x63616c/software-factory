package store_test

import (
	"context"
	"testing"

	"github.com/0x63616c/software-factory/internal/store"
)

func TestRecordWebhookDeliveryAcknowledgesOnceWithoutChangingTickets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ticket, err := s.CreateTicket(ctx, "target webhook", "delivery is informational", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	first, err := s.RecordWebhookDelivery(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("RecordWebhookDelivery(first): %v", err)
	}
	if !first {
		t.Fatal("first delivery = false, want true")
	}
	first, err = s.RecordWebhookDelivery(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("RecordWebhookDelivery(redelivery): %v", err)
	}
	if first {
		t.Fatal("redelivery = true, want false")
	}

	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketOpen {
		t.Fatalf("ticket state = %s, want unchanged open", got.State)
	}
}
