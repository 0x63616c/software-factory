package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/work"
)

func TestConsoleSnapshotDoesNotExposeRetiredDispatcherState(t *testing.T) {
	t.Parallel()
	fake := storefake.New()

	response := ticketRequest(t, New("test-build", nil, fake), http.MethodGet, "/v1/console", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/console = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode console snapshot: %v", err)
	}
	if _, ok := body["dispatcher"]; ok {
		t.Fatalf("console response = %s, must not expose retired dispatcher state", response.Body.String())
	}
}

func TestTicketAPIProjectsAndFiltersTargetActiveState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := storefake.New()
	ticket, err := fake.CreateTicket(ctx, "target-owned", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := "0f466627-b3ae-4ba2-9c96-6ef44ec6f578"
	if _, err := fake.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	response := ticketRequest(t, New("test-build", nil, fake), http.MethodGet, "/v1/tickets?state=active", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"active"`) {
		t.Fatalf("GET active tickets = (%d, %s), want active Ticket", response.Code, response.Body.String())
	}
}

type commandFake struct {
	updates  []work.ConfigUpdate
	workNow  []int
	canceled []int
	err      error
}

func TestTicketsCreateDependenciesAndReadiness(t *testing.T) {
	t.Parallel()
	fake := storefake.New()
	service := New("test-build", nil, fake)
	create := func(title string) int64 {
		t.Helper()
		response := ticketRequest(t, service, http.MethodPost, "/v1/tickets", `{"title":"`+title+`","body":"detail"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("create %s status = %d: %s", title, response.Code, response.Body.String())
		}
		var body struct {
			ID        int64  `json:"id"`
			State     string `json:"state"`
			CreatedAt string `json:"createdAt"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode create: %v", err)
		}
		if body.State != "open" || !strings.HasSuffix(body.CreatedAt, "Z") {
			t.Fatalf("created ticket = %#v, want open with UTC timestamp", body)
		}
		return body.ID
	}
	a, b, c := create("A"), create("B"), create("C")
	if response := ticketRequest(t, service, http.MethodPut, "/v1/tickets/2/blockers/1", ""); response.Code != http.StatusNoContent {
		t.Fatalf("add A -> B = %d: %s", response.Code, response.Body.String())
	}
	if response := ticketRequest(t, service, http.MethodPut, "/v1/tickets/2/blockers/1", ""); response.Code != http.StatusNoContent {
		t.Fatalf("idempotent A -> B = %d: %s", response.Code, response.Body.String())
	}
	if response := ticketRequest(t, service, http.MethodPut, "/v1/tickets/3/blockers/2", ""); response.Code != http.StatusNoContent {
		t.Fatalf("add B -> C = %d: %s", response.Code, response.Body.String())
	}
	response := ticketRequest(t, service, http.MethodPut, "/v1/tickets/1/blockers/3", "")
	if response.Code != http.StatusConflict || ticketErrorReason(t, response) != "cycle" {
		t.Fatalf("transitive cycle = (%d, %s), want distinguishable conflict", response.Code, response.Body.String())
	}
	response = ticketRequest(t, service, http.MethodPut, "/v1/tickets/1/blockers/1", "")
	if response.Code != http.StatusBadRequest || ticketErrorReason(t, response) != "self_dependency" {
		t.Fatalf("self edge = (%d, %s)", response.Code, response.Body.String())
	}
	response = ticketRequest(t, service, http.MethodPatch, "/v1/tickets/1/state", `{"state":"done"}`)
	if response.Code != http.StatusConflict || ticketErrorReason(t, response) != "illegal_transition" {
		t.Fatalf("illegal transition = (%d, %s)", response.Code, response.Body.String())
	}
	if a != 1 || b != 2 || c != 3 {
		t.Fatalf("ticket ids = %d, %d, %d, want 1, 2, 3", a, b, c)
	}
}

func TestCreateTicketAttachesDeclaredBlockersBeforeItIsVisible(t *testing.T) {
	t.Parallel()
	fake := storefake.New()
	service := New("test-build", nil, fake)

	upstream := ticketRequest(t, service, http.MethodPost, "/v1/tickets", `{"title":"upstream","body":"finish first"}`)
	if upstream.Code != http.StatusOK {
		t.Fatalf("create upstream = %d: %s", upstream.Code, upstream.Body.String())
	}
	var upstreamBody struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(upstream.Body).Decode(&upstreamBody); err != nil {
		t.Fatalf("decode upstream: %v", err)
	}

	downstream := ticketRequest(t, service, http.MethodPost, "/v1/tickets", `{"title":"downstream","body":"wait","blockedBy":[`+strconv.FormatInt(upstreamBody.ID, 10)+`]}`)
	if downstream.Code != http.StatusOK {
		t.Fatalf("create downstream = %d: %s", downstream.Code, downstream.Body.String())
	}
	var downstreamBody ticketResponse
	if err := json.NewDecoder(downstream.Body).Decode(&downstreamBody); err != nil {
		t.Fatalf("decode downstream: %v", err)
	}
	if downstreamBody.Ready || len(downstreamBody.Blockers) != 1 || downstreamBody.Blockers[0].ID != upstreamBody.ID {
		t.Fatalf("created downstream = %#v, want its upstream blocker and ready false", downstreamBody)
	}

	ready := ticketRequest(t, service, http.MethodGet, "/v1/tickets?ready=true", "")
	if ready.Code != http.StatusOK || strings.Contains(ready.Body.String(), `"id":2`) {
		t.Fatalf("ready tickets = (%d, %s), want downstream excluded", ready.Code, ready.Body.String())
	}
	for _, state := range []string{"working", "review"} {
		response := ticketRequest(t, service, http.MethodPatch, "/v1/tickets/"+strconv.FormatInt(upstreamBody.ID, 10)+"/state", `{"state":"`+state+`"}`)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("legacy state %s = %d: %s, want 422", state, response.Code, response.Body.String())
		}
	}
	if _, err := fake.UpdateTicketState(context.Background(), store.TicketID(upstreamBody.ID), store.TicketDone); err != nil {
		t.Fatalf("complete upstream fixture: %v", err)
	}
	ready = ticketRequest(t, service, http.MethodGet, "/v1/tickets?ready=true", "")
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"id":`+strconv.FormatInt(downstreamBody.ID, 10)) {
		t.Fatalf("ready tickets after upstream done = (%d, %s), want downstream included", ready.Code, ready.Body.String())
	}

	missing := ticketRequest(t, service, http.MethodPost, "/v1/tickets", `{"title":"missing blocker","body":"wait","blockedBy":[999]}`)
	if missing.Code != http.StatusNotFound || ticketErrorReason(t, missing) != "not_found" {
		t.Fatalf("create with missing blocker = (%d, %s), want not_found", missing.Code, missing.Body.String())
	}
}

// TestValidationAndUnexpectedErrorsCarryAReason proves every error response
// this package can produce satisfies the OpenAPI ErrorModel schema's required
// "reason" field — not only the ones this package deliberately raises through
// ticketError, but Huma's own request-validation failures and the built-in
// huma.ErrorNNN helpers used by the pre-existing factory-command routes. See
// the huma.NewError override in api.go.
func TestValidationAndUnexpectedErrorsCarryAReason(t *testing.T) {
	t.Parallel()
	service := New("test-build", nil, storefake.New())

	response := ticketRequest(t, service, http.MethodPatch, "/v1/tickets/1/state", `{"state":"not-a-state"}`)
	if response.Code != http.StatusUnprocessableEntity || ticketErrorReason(t, response) == "" {
		t.Fatalf("malformed state = (%d, %s), want a 422 carrying a reason", response.Code, response.Body.String())
	}

	response = ticketRequest(t, service, http.MethodPost, "/v1/factory/max-in-flight", `{"maxInFlight":0}`)
	if response.Code != http.StatusUnprocessableEntity || ticketErrorReason(t, response) == "" {
		t.Fatalf("out-of-range maxInFlight = (%d, %s), want a 422 carrying a reason", response.Code, response.Body.String())
	}

	unconfigured := New("test-build", nil)
	response = ticketRequest(t, unconfigured, http.MethodGet, "/v1/tickets/1", "")
	if response.Code != http.StatusServiceUnavailable || ticketErrorReason(t, response) != "store_unavailable" {
		t.Fatalf("unconfigured store = (%d, %s), want store_unavailable", response.Code, response.Body.String())
	}
}

func ticketRequest(t *testing.T, service *Service, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	return response
}

func ticketErrorReason(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body.Reason
}

func (fake *commandFake) UpdateConfig(_ context.Context, update work.ConfigUpdate) error {
	fake.updates = append(fake.updates, update)
	return fake.err
}

func (fake *commandFake) WorkNow(_ context.Context, ticketID int) error {
	fake.workNow = append(fake.workNow, ticketID)
	return fake.err
}

func (fake *commandFake) CancelTicket(_ context.Context, ticketID int) error {
	fake.canceled = append(fake.canceled, ticketID)
	return fake.err
}

func TestBuildEndpointAndOpenAPI(t *testing.T) {
	service := New("test-build", nil)

	request := httptest.NewRequest(http.MethodGet, "/v1/build", nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/build status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); !strings.Contains(got, "test-build") {
		t.Fatalf("GET /v1/build body = %q, want build version", got)
	}

	spec, err := service.OpenAPIYAML()
	if err != nil {
		t.Fatalf("OpenAPIYAML() error = %v", err)
	}
	if !strings.Contains(string(spec), "openapi: 3.1.0") || !strings.Contains(string(spec), "/v1/build:") {
		t.Fatalf("OpenAPIYAML() = %s, want OpenAPI 3.1 build path", spec)
	}
	for _, requirement := range []string{
		"cloudflareAccess:", "Cf-Access-Jwt-Assertion", "inClusterBearer:", "scheme: bearer",
		"agentCheckpointCapability:", "X-Software-Factory-Checkpoint-Capability",
		"/v1/run-worker/runs/{runID}/steps/{stepOrdinal}/attempts/{attemptNo}/checkpoint:",
		"repositoryCheckpointCapability:", "X-Software-Factory-Repository-Capability",
		"/v1/run-worker/runs/{runID}/generations/{generation}/repository-checkpoint:",
	} {
		if !strings.Contains(string(spec), requirement) {
			t.Fatalf("OpenAPIYAML() missing security requirement %q", requirement)
		}
	}
}

func TestCommandsTranslateHTTPRequestsToDispatcherCommands(t *testing.T) {
	t.Parallel()

	commands := &commandFake{}
	service := New("test-build", commands)
	for _, test := range []struct {
		name   string
		path   string
		body   string
		assert func(*testing.T)
	}{
		{
			name: "pause", path: "/v1/factory/pause",
			assert: func(t *testing.T) {
				if len(commands.updates) != 1 || commands.updates[0].Paused == nil || !*commands.updates[0].Paused {
					t.Fatalf("updates = %#v, want paused config update", commands.updates)
				}
			},
		},
		{
			name: "resume", path: "/v1/factory/resume",
			assert: func(t *testing.T) {
				if len(commands.updates) != 2 || commands.updates[1].Paused == nil || *commands.updates[1].Paused {
					t.Fatalf("updates = %#v, want resumed config update", commands.updates)
				}
			},
		},
		{
			name: "max in flight", path: "/v1/factory/max-in-flight", body: `{"maxInFlight":4}`,
			assert: func(t *testing.T) {
				if len(commands.updates) != 3 || commands.updates[2].MaxInFlight == nil || *commands.updates[2].MaxInFlight != 4 {
					t.Fatalf("updates = %#v, want max-in-flight config update", commands.updates)
				}
			},
		},
		{
			name: "work now", path: "/v1/tickets/42/work",
			assert: func(t *testing.T) {
				if len(commands.updates) != 3 || len(commands.workNow) != 1 || commands.workNow[0] != 42 {
					t.Fatalf("updates = %#v, work-now = %#v, want acknowledged Ticket 42 request", commands.updates, commands.workNow)
				}
			},
		},
		{
			name: "cancel", path: "/v1/tickets/42/cancel",
			assert: func(t *testing.T) {
				if len(commands.canceled) != 1 || commands.canceled[0] != 42 {
					t.Fatalf("canceled = %#v, want ticket 42", commands.canceled)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("POST %s status = %d, want %d: %s", test.path, response.Code, http.StatusNoContent, response.Body.String())
			}
			test.assert(t)
		})
	}
}

func TestCommandsMapWorkflowFailuresToHTTPResponses(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"unknown workflow", work.ErrWorkflowNotFound, http.StatusNotFound},
		{"closed workflow", work.ErrWorkflowClosed, http.StatusConflict},
		{"transient failure", errors.New("Temporal unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := New("test-build", &commandFake{err: test.err})
			request := httptest.NewRequest(http.MethodPost, "/v1/tickets/42/cancel", nil)
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}
