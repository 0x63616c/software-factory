package sf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientBuildEndpoint(t *testing.T) {
	client, err := NewClient("https://example.com/some/path", Credentials{}, 10*time.Second, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := client.BuildEndpoint("/v1/tickets"); got != "https://example.com/v1/tickets" {
		t.Fatalf("unexpected endpoint: %q", got)
	}
}

func TestNewClientAddsBearerAuthorizationHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tickets": []}`))
	}))
	defer ts.Close()

	client, err := NewClient(ts.URL, Credentials{BearerToken: "bearer-token"}, 0, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected client error: %v", err)
	}
	if _, _, err := client.doRaw(context.Background(), http.MethodGet, "/v1/console", nil); err != nil {
		t.Fatalf("unexpected request error: %v", err)
	}
	if gotAuth != "Bearer bearer-token" {
		t.Fatalf("expected bearer header, got %q", gotAuth)
	}
}

func TestNewClientAddsCloudflareAssertionHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Cf-Access-Jwt-Assertion")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tickets": []}`))
	}))
	defer ts.Close()

	client, err := NewClient(ts.URL, Credentials{CfAccessToken: "cf-token"}, 0, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected client error: %v", err)
	}
	if _, _, err := client.doRaw(context.Background(), http.MethodGet, "/v1/console", nil); err != nil {
		t.Fatalf("unexpected request error: %v", err)
	}
	if gotAuth != "cf-token" {
		t.Fatalf("expected CF assertion header, got %q", gotAuth)
	}
}

func TestListTicketsBuildsExpectedQuery(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		response := struct {
			Tickets []TicketSummary `json:"tickets"`
		}{
			Tickets: []TicketSummary{},
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
	ctx := context.Background()
	if _, err := client.ListTickets(ctx, "open", "true"); err != nil {
		t.Fatalf("list tickets failed: %v", err)
	}
	if gotPath != "/v1/tickets" {
		t.Fatalf("unexpected ticket list request path: %q", gotPath)
	}
	if gotQuery != "state=open&ready=true" && gotQuery != "ready=true&state=open" {
		t.Fatalf("unexpected ticket list request path: %q", gotPath)
	}
}

func TestParseErrorResponseMapsToReason(t *testing.T) {
	statusText := parseErrorResponse(http.StatusConflict, []byte(`{"reason":"conflict","detail":"already exists"}`))
	parsed, ok := statusText.(APIError)
	if !ok {
		t.Fatalf("expected APIError, got %T", statusText)
	}
	if parsed.Reason != "conflict" {
		t.Fatalf("expected conflict reason, got %q", parsed.Reason)
	}
	if parsed.Detail != "already exists" {
		t.Fatalf("unexpected detail: %q", parsed.Detail)
	}
}

func TestParseErrorResponsePreservesBackendReasonForMachine(t *testing.T) {
	statusText := parseErrorResponse(http.StatusBadRequest, []byte(`{"reason":"illegal_transition","detail":"cannot move from active to open"}`))
	parsed, ok := statusText.(APIError)
	if !ok {
		t.Fatalf("expected APIError, got %T", statusText)
	}
	if parsed.Reason != "illegal_transition" {
		t.Fatalf("expected illegal_transition reason, got %q", parsed.Reason)
	}
	if parsed.Detail != "cannot move from active to open" {
		t.Fatalf("unexpected detail: %q", parsed.Detail)
	}
}

func TestExitCodeMapsKnownReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   int
	}{
		{reason: "not_found", want: 12},
		{reason: "conflict", want: 10},
		{reason: "illegal_transition", want: 10},
		{reason: "workflow_closed", want: 10},
		{reason: "workflow_not_found", want: 11},
		{reason: "invalid_request", want: 13},
		{reason: "unavailable", want: 14},
		{reason: "commands_unavailable", want: 15},
		{reason: "unknown", want: 1},
	}
	for _, c := range cases {
		got := ExitCode(APIError{Reason: c.reason})
		if got != c.want {
			t.Fatalf("reason %q: expected %d, got %d", c.reason, c.want, got)
		}
	}
}
