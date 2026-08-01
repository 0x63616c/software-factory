package sf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActionsStatusAggregatesTickets(t *testing.T) {
	seen := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.Method != http.MethodGet || r.URL.Path != "/v1/console" {
			t.Fatalf("expected GET /v1/console, got %s %s", r.Method, r.URL.Path)
		}
		response := struct {
			Tickets []TicketSummary `json:"tickets"`
		}{
			Tickets: []TicketSummary{
				{ID: 1, State: string(TicketStateOpen), Ready: true},
				{ID: 2, State: string(TicketStateActive), Ready: false},
				{ID: 3, State: string(TicketStateFailed), Ready: false},
				{ID: 4, State: string(TicketStateDone), Ready: true},
			},
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("unexpected marshal error: %v", err)
		}
		_, _ = w.Write(encoded)
	}))
	defer ts.Close()

	client, err := NewClient(ts.URL, Credentials{BearerToken: "token"}, 0, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected client error: %v", err)
	}
	actions := &Actions{Client: client}

	got, err := actions.Status(context.Background())
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !seen {
		t.Fatalf("console endpoint was not called")
	}
	if got.Open != 1 || got.Active != 1 || got.Failed != 1 || got.Done != 1 {
		t.Fatalf("unexpected status counts: %+v", got)
	}
	if got.Ready != 2 || got.NotReady != 2 {
		t.Fatalf("unexpected readiness counts: %+v", got)
	}
}

func TestActionsSetTicketStateHitsEndpoint(t *testing.T) {
	var gotState string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("expected PATCH, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tickets/42/state" {
			t.Fatalf("expected /v1/tickets/42/state, got %s", r.URL.Path)
		}
		var request struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotState = request.State
		w.Header().Set("Content-Type", "application/json")
		response := TicketResponse{ID: 42, State: gotState}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("unexpected marshal error: %v", err)
		}
		_, _ = w.Write(encoded)
	}))
	defer ts.Close()

	client, err := NewClient(ts.URL, Credentials{BearerToken: "token"}, 0, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected client error: %v", err)
	}
	actions := &Actions{Client: client}
	_, err = actions.SetTicketState(context.Background(), 42, string(TicketStateDone))
	if err != nil {
		t.Fatalf("set state failed: %v", err)
	}
	if gotState != string(TicketStateDone) {
		t.Fatalf("expected state %q, got %q", TicketStateDone, gotState)
	}
}

func TestActionsWorkAndCancelSendExpectedRequests(t *testing.T) {
	var workCalled, cancelCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tickets/9/work":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST /v1/tickets/9/work, got %s", r.Method)
			}
			workCalled = true
			w.WriteHeader(http.StatusNoContent)
		case "/v1/tickets/9/cancel":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST /v1/tickets/9/cancel, got %s", r.Method)
			}
			cancelCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	client, err := NewClient(ts.URL, Credentials{BearerToken: "token"}, 0, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected client error: %v", err)
	}
	actions := &Actions{Client: client}
	if err := actions.WorkTicket(context.Background(), 9); err != nil {
		t.Fatalf("work ticket failed: %v", err)
	}
	if err := actions.CancelTicket(context.Background(), 9); err != nil {
		t.Fatalf("cancel ticket failed: %v", err)
	}
	if !workCalled || !cancelCalled {
		t.Fatalf("expected both endpoints called (work=%v cancel=%v)", workCalled, cancelCalled)
	}
}
