package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/work"
)

const testSecret = "test-webhook-secret"

func TestValidSignatureAndMergedPullRequestMarksTicketDoneAndFreesDownstream(t *testing.T) {
	t.Parallel()

	fake := storefake.New()
	blocker := mustTicket(t, fake, store.TicketReview)
	blocked := mustTicket(t, fake, store.TicketOpen)
	if err := fake.AddTicketDependency(context.Background(), blocker.ID, blocked.ID); err != nil {
		t.Fatalf("AddTicketDependency: %v", err)
	}

	logBuf := &bytes.Buffer{}
	handler := NewHandler([]byte(testSecret), fake, testLogger(logBuf), prometheus.NewRegistry())

	body := mergedPullRequestBody(t, blocker.ID, "run-1", true)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(body, "delivery-1", "pull_request"))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body %q", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
	ticket, err := fake.Ticket(context.Background(), blocker.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if ticket.State != store.TicketDone {
		t.Fatalf("blocker state = %s, want done", ticket.State)
	}

	ready, err := fake.ReadyTickets(context.Background())
	if err != nil {
		t.Fatalf("ReadyTickets: %v", err)
	}
	if !containsTicket(ready, blocked.ID) {
		t.Fatalf("ReadyTickets() = %+v, want it to contain the downstream ticket %d now its blocker is done", ready, blocked.ID)
	}
}

func TestClosedWithoutMergeFailsTheTicket(t *testing.T) {
	t.Parallel()

	fake := storefake.New()
	ticket := mustTicket(t, fake, store.TicketReview)
	handler := NewHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())

	body := mergedPullRequestBody(t, ticket.ID, "run-1", false)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(body, "delivery-1", "pull_request"))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	got, err := fake.Ticket(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketFailed {
		t.Fatalf("ticket state = %s, want failed", got.State)
	}
}

func TestInvalidSignatureIsRejectedBeforeAnyStoreAccessAndIsUniform(t *testing.T) {
	t.Parallel()

	fake := storefake.New()
	ticket := mustTicket(t, fake, store.TicketReview)
	handler := NewHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())

	body := mergedPullRequestBody(t, ticket.ID, "run-1", true)

	wrongSecret := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", bytes.NewReader(body))
	wrongSecret.Header.Set("x-hub-signature-256", signature(body, "not-the-secret"))
	wrongSecret.Header.Set("x-github-delivery", "delivery-1")
	wrongSecret.Header.Set("x-github-event", "pull_request")

	malformed := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", bytes.NewReader(body))
	malformed.Header.Set("x-hub-signature-256", "not-a-valid-signature")
	malformed.Header.Set("x-github-delivery", "delivery-2")
	malformed.Header.Set("x-github-event", "pull_request")

	missing := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", bytes.NewReader(body))
	missing.Header.Set("x-github-delivery", "delivery-3")
	missing.Header.Set("x-github-event", "pull_request")

	var bodies []string
	for _, request := range []*http.Request{wrongSecret, malformed, missing} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
		}
		bodies = append(bodies, recorder.Body.String())
	}
	for index, got := range bodies {
		if got != bodies[0] {
			t.Fatalf("rejection body %d = %q, want identical to %q for every rejection reason", index, got, bodies[0])
		}
	}

	got, err := fake.Ticket(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketReview {
		t.Fatalf("ticket state = %s, want unchanged (review) — an invalid signature must never touch the store", got.State)
	}
}

func TestRedeliveryOfTheSameDeliveryIDIsANoOp(t *testing.T) {
	t.Parallel()

	fake := storefake.New()
	ticket := mustTicket(t, fake, store.TicketReview)
	handler := NewHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())

	body := mergedPullRequestBody(t, ticket.ID, "run-1", true)
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedRequest(body, "delivery-1", "pull_request"))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	}

	got, err := fake.Ticket(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketDone {
		t.Fatalf("ticket state = %s, want done", got.State)
	}
}

func TestUnrelatedEventIsAcceptedAndIgnored(t *testing.T) {
	t.Parallel()

	fake := storefake.New()
	ticket := mustTicket(t, fake, store.TicketReview)
	handler := NewHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())

	body := []byte(`{"zen": "keep it logically awesome"}`)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(body, "delivery-1", "ping"))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	got, err := fake.Ticket(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketReview {
		t.Fatalf("ticket state = %s, want unchanged", got.State)
	}
}

func TestUnrelatedPullRequestActionIsAcceptedAndIgnored(t *testing.T) {
	t.Parallel()

	fake := storefake.New()
	ticket := mustTicket(t, fake, store.TicketReview)
	handler := NewHandler([]byte(testSecret), fake, testLogger(io.Discard), prometheus.NewRegistry())

	body := []byte(fmt.Sprintf(`{"action":"opened","pull_request":{"merged":false,"head":{"ref":%q}}}`,
		work.FactoryTicketBranchName(int64(ticket.ID), "run-1")))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(body, "delivery-1", "pull_request"))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	got, err := fake.Ticket(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketReview {
		t.Fatalf("ticket state = %s, want unchanged", got.State)
	}
}

func TestNoPayloadBodyAppearsInAnyLogLine(t *testing.T) {
	t.Parallel()

	fake := storefake.New()
	ticket := mustTicket(t, fake, store.TicketReview)
	logBuf := &bytes.Buffer{}
	handler := NewHandler([]byte(testSecret), fake, testLogger(logBuf), prometheus.NewRegistry())

	const canary = "do-not-log-this-repository-content"
	body := []byte(fmt.Sprintf(
		`{"action":"closed","pull_request":{"merged":true,"head":{"ref":%q},"body":%q}}`,
		work.FactoryTicketBranchName(int64(ticket.ID), "run-1"), canary,
	))
	handler.ServeHTTP(httptest.NewRecorder(), signedRequest(body, "delivery-1", "pull_request"))

	if strings.Contains(logBuf.String(), canary) {
		t.Fatalf("log output contains payload body content: %s", logBuf.String())
	}
}

func TestMethodNotAllowedForNonPost(t *testing.T) {
	t.Parallel()

	handler := NewHandler([]byte(testSecret), storefake.New(), testLogger(io.Discard), prometheus.NewRegistry())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/hooks/github", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func mustTicket(t *testing.T, fake *storefake.Store, state store.TicketState) store.Ticket {
	t.Helper()
	ticket, err := fake.CreateTicket(context.Background(), "a ticket", "body", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if state == store.TicketOpen {
		return ticket
	}
	// Walk the legal transition chain rather than writing the state directly,
	// so this fixture can never construct a Ticket state real code could not
	// reach.
	chain := map[store.TicketState][]store.TicketState{
		store.TicketWorking: {store.TicketOpen, store.TicketWorking},
		store.TicketReview:  {store.TicketOpen, store.TicketWorking, store.TicketReview},
	}[state]
	for i := 1; i < len(chain); i++ {
		var err error
		ticket, err = fake.TransitionTicketState(context.Background(), ticket.ID, chain[i-1], chain[i])
		if err != nil {
			t.Fatalf("TransitionTicketState %s -> %s: %v", chain[i-1], chain[i], err)
		}
	}
	return ticket
}

func containsTicket(tickets []store.Ticket, id store.TicketID) bool {
	for _, ticket := range tickets {
		if ticket.ID == id {
			return true
		}
	}
	return false
}

func mergedPullRequestBody(t *testing.T, ticketID store.TicketID, runID string, merged bool) []byte {
	t.Helper()
	payload := map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"merged": merged,
			"head": map[string]any{
				"ref": work.FactoryTicketBranchName(int64(ticketID), runID),
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return body
}

func signedRequest(body []byte, deliveryID, event string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", bytes.NewReader(body))
	request.Header.Set("x-hub-signature-256", signature(body, testSecret))
	request.Header.Set("x-github-delivery", deliveryID)
	request.Header.Set("x-github-event", event)
	return request
}

func signature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func testLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}
