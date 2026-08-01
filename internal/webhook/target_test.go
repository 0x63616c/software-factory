package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
)

func TestTargetHandlerAuthenticatesAndRecordsPullRequestClosedWithoutChangingTicketState(t *testing.T) {
	t.Parallel()
	for _, merged := range []bool{true, false} {
		merged := merged
		t.Run(map[bool]string{true: "merged", false: "unmerged"}[merged], func(t *testing.T) {
			t.Parallel()
			fake := storefake.New()
			ticket := mustTicket(t, fake, store.TicketOpen)
			handler := NewTargetHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())
			body := mergedPullRequestBody(t, ticket.ID, "run-1", merged)

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, signedRequest(body, "target-delivery", "pull_request"))
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
			got, err := fake.Ticket(context.Background(), ticket.ID)
			if err != nil {
				t.Fatalf("Ticket: %v", err)
			}
			if got.State != store.TicketOpen {
				t.Fatalf("ticket state = %s, want unchanged open", got.State)
			}
		})
	}
}

func TestTargetHandlerRedeliveryAndUnrelatedEventsAreAcceptedIdempotently(t *testing.T) {
	t.Parallel()
	fake := storefake.New()
	handler := NewTargetHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())

	body := []byte(`{"zen":"keep it logically awesome"}`)
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedRequest(body, "ping-delivery", "ping"))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	}
	first, err := fake.RecordWebhookDelivery(context.Background(), "ping-delivery")
	if err != nil {
		t.Fatalf("RecordWebhookDelivery probe: %v", err)
	}
	if first {
		t.Fatal("delivery was not durably recorded by the handler")
	}
}

func TestTargetHandlerRejectsInvalidSignatureBeforeRecordingDelivery(t *testing.T) {
	t.Parallel()
	fake := storefake.New()
	handler := NewTargetHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())
	request := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", nil)
	request.Header.Set("x-github-delivery", "invalid-delivery")
	request.Header.Set("x-github-event", "pull_request")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	first, err := fake.RecordWebhookDelivery(context.Background(), "invalid-delivery")
	if err != nil {
		t.Fatalf("RecordWebhookDelivery probe: %v", err)
	}
	if !first {
		t.Fatal("invalid delivery was recorded before authentication")
	}
}
