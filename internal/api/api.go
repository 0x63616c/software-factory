// Package api owns the factory's HTTP contract and Huma integration.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// Service is the narrow API boundary exposed to composition roots.
type Service struct {
	handler               http.Handler
	api                   huma.API
	commands              commandClient
	tickets               factoryStore
	checkpoints           AgentCheckpointStore
	repositoryCheckpoints RepositoryCheckpointStore
}

// factoryStore is the small persistence door the whole HTTP contract needs:
// Tickets and their dependency graph, plus the Runs, Steps, Attempts and
// transcripts recorded against them (ADR-0012's console detail view).
type factoryStore interface {
	store.TicketCreator
	store.TicketReader
	store.TicketStateWriter
	store.ReadyTicketLister
	store.TicketDependencyWriter
	store.TicketDependencyReader
	store.RunLister
	store.RunReader
	store.TargetHistoryReader
}

// commandClient is the factory command surface the HTTP handlers need.
// Temporal stays sealed in its client package so browser-facing code never
// learns its SDK vocabulary or connection details.
type commandClient interface {
	UpdateConfig(context.Context, work.ConfigUpdate) error
	WorkNow(context.Context, int) error
	CancelTicket(context.Context, int) error
}

type buildOutput struct {
	Body struct {
		Version string `json:"version" doc:"The build version running this API."`
	}
}

type maxInFlightInput struct {
	Body struct {
		MaxInFlight int `json:"maxInFlight" minimum:"1" doc:"Maximum number of ticket runs the dispatcher may start at once."`
	}
}

type ticketInput struct {
	TicketID int `path:"ticketID" minimum:"1" doc:"The ticket number whose run is being commanded."`
}

type createTicketInput struct {
	Body struct {
		Title     string  `json:"title" minLength:"1" doc:"The concise title describing the Ticket work."`
		Body      string  `json:"body" doc:"The Ticket's supporting detail."`
		BlockedBy []int64 `json:"blockedBy,omitempty" doc:"Tickets that must be done before this Ticket is ready."`
	}
}

type stateTicketInput struct {
	TicketID int64 `path:"ticketID" minimum:"1" doc:"The Ticket identifier."`
	Body     struct {
		State store.TicketState `json:"state" enum:"open,active,done,failed" doc:"The lifecycle state. Active is owned by a target Run."`
	}
}

type ticketPathInput struct {
	TicketID        int64 `path:"ticketID" minimum:"1" doc:"The Ticket identifier that is blocked."`
	BlockerTicketID int64 `path:"blockerTicketID" minimum:"1" doc:"The Ticket identifier that must be done first."`
}

type getTicketInput struct {
	TicketID int64 `path:"ticketID" minimum:"1" doc:"The Ticket identifier."`
}

type listTicketsInput struct {
	State store.TicketState `query:"state" enum:"open,active,done,failed" doc:"Limit results to this lifecycle state."`
	Ready string            `query:"ready" doc:"Limit results by derived readiness: true or false."`
}

type ticketOutput struct {
	Body ticketResponse
}
type ticketsOutput struct {
	Body struct {
		Tickets []ticketSummary `json:"tickets" doc:"Tickets matching the requested filters."`
	}
}

type consoleOutput struct{ Body consoleResponse }

// consoleResponse is the one-screen, read-only operational view of the factory.
type consoleResponse struct {
	Tickets []ticketSummary `json:"tickets"`
}

// ticketSummary is the list representation of a Ticket.
type ticketSummary struct {
	ID        int64  `json:"id" doc:"The Ticket identifier."`
	Title     string `json:"title" doc:"The Ticket title."`
	State     string `json:"state" doc:"The Ticket lifecycle state."`
	Ready     bool   `json:"ready" doc:"Whether this open Ticket has only done dependencies."`
	CreatedAt string `json:"createdAt" doc:"The Ticket creation time in RFC3339 UTC."`
	UpdatedAt string `json:"updatedAt" doc:"The Ticket's latest update time in RFC3339 UTC."`
}

// ticketResponse is the detailed Ticket representation.
type ticketResponse struct {
	ID        int64           `json:"id" doc:"The Ticket identifier."`
	Title     string          `json:"title" doc:"The Ticket title."`
	Body      string          `json:"body" doc:"The Ticket's supporting detail."`
	State     string          `json:"state" doc:"The Ticket lifecycle state."`
	Ready     bool            `json:"ready" doc:"Whether this open Ticket has only done dependencies."`
	Blockers  []ticketSummary `json:"blockers" doc:"Tickets that must be done before this Ticket is ready."`
	Blocks    []ticketSummary `json:"blocks" doc:"Tickets this Ticket prevents from becoming ready."`
	CreatedAt string          `json:"createdAt" doc:"The Ticket creation time in RFC3339 UTC."`
	UpdatedAt string          `json:"updatedAt" doc:"The Ticket's latest update time in RFC3339 UTC."`
}

// ticketError extends the API problem response with a stable client reason.
type ticketError struct {
	huma.ErrorModel
	Reason string `json:"reason" doc:"Stable machine-readable reason for the error."`
}

// defaultHumaNewError is Huma's own NewError, captured once before init
// overrides the package variable below, so the override can still build on
// top of Huma's default ErrorModel construction rather than reimplement it.
var defaultHumaNewError = huma.NewError

func init() {
	// The generated OpenAPI schema requires "reason" on every error response
	// (see New, below), but most errors never reach this package's own
	// ticketError/clientError constructors: request validation (a malformed
	// body, an unparsable path or query value, a missing required field) and
	// any built-in huma.ErrorNNN helper are constructed by Huma itself via
	// huma.NewError. Overriding it here is Huma's documented extension point
	// (see huma.NewError's doc comment) and is the only way to guarantee every
	// error response — not just the ones this package deliberately raises —
	// satisfies the schema it commits to.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		model := defaultHumaNewError(status, msg, errs...)
		if status == 0 {
			// Huma's own operation-registration code calls NewError(0, "") once
			// per operation purely to learn the wire error type by reflection
			// (huma.go's defineErrors), never to build a real response. Keep
			// returning its own *huma.ErrorModel there so that reflection keeps
			// naming the shared OpenAPI schema "ErrorModel" — the name New,
			// below, adds "reason" to. Every real call below has a non-zero
			// status and gets wrapped.
			return model
		}
		errorModel, ok := model.(*huma.ErrorModel)
		if !ok {
			return model
		}
		return &ticketError{ErrorModel: *errorModel, Reason: reasonForStatus(status)}
	}
}

// reasonForStatus is the fallback reason for an error this package did not
// construct with a specific one of its own — request validation failures and
// any bare huma.ErrorNNN call. It is deliberately coarse-grained: a caller
// that needs a precise reason (illegal_transition, cycle, self_dependency,
// not_found) gets one from clientError/ticketStoreError instead.
func reasonForStatus(status int) string {
	switch {
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusServiceUnavailable:
		return "unavailable"
	case status >= 500:
		return "internal"
	default:
		return "invalid_request"
	}
}

// New constructs the complete HTTP API. Version arrives from the composition
// root so this package does not need to learn about build metadata policy.
func New(version string, commands commandClient, ticketStores ...factoryStore) *Service {
	mux := http.NewServeMux()
	configuration := huma.DefaultConfig("Software Factory API", version)
	configuration.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"cloudflareAccess": {
			Type: "apiKey", In: "header", Name: "Cf-Access-Jwt-Assertion",
			Description: "Cloudflare Access JWT assertion.",
		},
		"inClusterBearer": {
			Type: "http", Scheme: "bearer",
			Description: "Static bearer for in-cluster worker or Run Worker callers.",
		},
		"agentCheckpointCapability": {
			Type: "apiKey", In: "header", Name: checkpoint.CapabilityHeader,
			Description: "A capability scoped to one active Agent Attempt.",
		},
		"repositoryCheckpointCapability": {
			Type: "apiKey", In: "header", Name: checkpoint.RepositoryCapabilityHeader,
			Description: "A capability scoped to one active Run Worker generation.",
		},
	}
	configuration.Security = []map[string][]string{{"cloudflareAccess": {}}, {"inClusterBearer": {}}}
	api := humago.New(mux, configuration)
	service := &Service{handler: mux, api: api, commands: commands}
	if len(ticketStores) > 0 {
		service.tickets = ticketStores[0]
	}
	huma.Get(api, "/v1/build", func(_ context.Context, _ *struct{}) (*buildOutput, error) {
		output := &buildOutput{}
		output.Body.Version = version
		return output, nil
	})
	huma.Put(api, checkpoint.Path, service.checkpointAgentAttempt, checkpointOperation)
	huma.Get(api, checkpoint.Path, service.loadAgentAttemptCheckpoint, readCheckpointOperation)
	huma.Put(api, checkpoint.RepositoryPath, service.checkpointRepository, repositoryCheckpointOperation)
	huma.Patch(api, checkpoint.RepositoryPath, service.checkpointRepositoryEffect, repositoryEffectCheckpointOperation)
	huma.Get(api, checkpoint.RepositoryPath, service.loadRepositoryCheckpoint, readRepositoryCheckpointOperation)
	huma.Get(api, "/v1/console", service.console, commandOperation("Read console snapshot", "Returns the factory Tickets for the console."))
	huma.Post(api, "/v1/factory/pause", service.pause, commandOperation("Pause the factory", "Success means the target Dispatcher acknowledged the update; the requested policy is current when this returns. Any outstanding dispatch poll is canceled so the policy takes effect immediately."))
	huma.Post(api, "/v1/factory/resume", service.resume, commandOperation("Resume the factory", "Success means the target Dispatcher acknowledged the update; the requested policy is current when this returns. Any outstanding dispatch poll is canceled so the policy takes effect immediately."))
	huma.Post(api, "/v1/factory/max-in-flight", service.setMaxInFlight, commandOperation("Set factory max in flight", "Success means the target Dispatcher acknowledged the update; the requested policy is current when this returns. Any outstanding dispatch poll is canceled so the policy takes effect immediately."))
	huma.Post(api, "/v1/tickets/{ticketID}/cancel", service.cancelTicket, commandOperation("Cancel a ticket run", "Success means Temporal accepted cancellation of the ticket workflow. This endpoint does not wait for cleanup or database state to become observable."))
	huma.Post(api, "/v1/tickets/{ticketID}/work", service.workNow, commandOperation("Nudge the factory", "Success means the target Dispatcher acknowledged this Ticket-specific work-now request and scheduled an immediate re-evaluation. Readiness, capacity, and the current admission policy still determine whether the Ticket starts."))
	huma.Post(api, "/v1/tickets", service.createTicket, commandOperation("Create a Ticket", "Files a new open Ticket."))
	huma.Get(api, "/v1/tickets/{ticketID}", service.getTicket, commandOperation("Read a Ticket", "Returns the Ticket, both dependency directions, and derived readiness."))
	huma.Get(api, "/v1/tickets", service.listTickets, commandOperation("List Tickets", "Lists Tickets with optional state and derived ready filters."))
	huma.Patch(api, "/v1/tickets/{ticketID}/state", service.updateTicketState, commandOperation("Update Ticket state", "Moves a Ticket through its legal lifecycle transitions."))
	huma.Put(api, "/v1/tickets/{ticketID}/blockers/{blockerTicketID}", service.addBlocker, commandOperation("Add a Ticket blocker", "Records that the first Ticket is blocked by the second."))
	huma.Delete(api, "/v1/tickets/{ticketID}/blockers/{blockerTicketID}", service.removeBlocker, commandOperation("Remove a Ticket blocker", "Removes a dependency edge when it exists."))
	huma.Get(api, "/v1/tickets/{ticketID}/runs", service.getTicketRuns, commandOperation("List a Ticket's Runs", "Returns every Run of the Ticket, most recent first, each with its Steps and Attempts and rolled-up token usage."))
	huma.Get(api, "/v1/tickets/{ticketID}/runs/{runID}/steps/{ordinal}/attempts/{attemptNo}/transcript", service.getTargetAttemptTranscript, commandOperation("Download a target Attempt's transcript", "Returns an ordinal Step Attempt's raw JSONL event stream, decompressed, as a downloadable file."))
	errorSchema := api.OpenAPI().Components.Schemas.Map()["ErrorModel"]
	errorSchema.Properties["reason"] = &huma.Schema{Type: "string", Description: "Stable machine-readable reason for the error."}
	errorSchema.Required = append(errorSchema.Required, "reason")
	return service
}

// NewWithCheckpointStore constructs the API with only the exact-attempt checkpoint write authority.
func NewWithCheckpointStore(version string, commands commandClient, checkpoints AgentCheckpointStore, ticketStores ...factoryStore) *Service {
	service := New(version, commands, ticketStores...)
	service.checkpoints = checkpoints
	return service
}

// NewWithRunWorkerStores constructs the API with both narrow Run Worker
// checkpoint authorities and no broad database capability.
func NewWithRunWorkerStores(version string, commands commandClient, attempts AgentCheckpointStore, repositories RepositoryCheckpointStore, ticketStores ...factoryStore) *Service {
	service := NewWithCheckpointStore(version, commands, attempts, ticketStores...)
	service.repositoryCheckpoints = repositories
	return service
}

func (service *Service) console(ctx context.Context, _ *struct{}) (*consoleOutput, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	tickets, err := service.tickets.Tickets(ctx)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	ready, err := service.tickets.ReadyTickets(ctx)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	readySet := make(map[store.TicketID]bool, len(ready))
	for _, ticket := range ready {
		readySet[ticket.ID] = true
	}
	output := &consoleOutput{Body: consoleResponse{}}
	for _, ticket := range tickets {
		output.Body.Tickets = append(output.Body.Tickets, ticketSummaryFrom(ticket, readySet[ticket.ID]))
	}
	return output, nil
}

func (service *Service) createTicket(ctx context.Context, input *createTicketInput) (*ticketOutput, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	blockers := make([]store.TicketID, 0, len(input.Body.BlockedBy))
	for _, id := range input.Body.BlockedBy {
		blocker := store.TicketID(id)
		if _, err := service.ticket(ctx, blocker); err != nil {
			return nil, ticketStoreError(err)
		}
		blockers = append(blockers, blocker)
	}
	ticket, err := service.tickets.CreateTicket(ctx, input.Body.Title, input.Body.Body, blockers)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	return service.ticketOutput(ctx, ticket)
}

func (service *Service) getTicket(ctx context.Context, input *getTicketInput) (*ticketOutput, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	ticket, err := service.ticket(ctx, store.TicketID(input.TicketID))
	if err != nil {
		return nil, ticketStoreError(err)
	}
	return service.ticketOutput(ctx, ticket)
}

func (service *Service) updateTicketState(ctx context.Context, input *stateTicketInput) (*ticketOutput, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	current, err := service.ticket(ctx, store.TicketID(input.TicketID))
	if err != nil {
		return nil, ticketStoreError(err)
	}
	if !current.State.CanTransitionTo(input.Body.State) {
		return nil, clientError(http.StatusConflict, "illegal_transition", fmt.Sprintf("cannot transition ticket %d from %s to %s", current.ID, current.State, input.Body.State))
	}
	ticket, err := service.tickets.TransitionTicketState(ctx, current.ID, current.State, input.Body.State)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, clientError(http.StatusConflict, "illegal_transition", "Ticket state changed before the transition could be recorded")
		}
		return nil, ticketStoreError(err)
	}
	return service.ticketOutput(ctx, ticket)
}

func (service *Service) addBlocker(ctx context.Context, input *ticketPathInput) (*struct{}, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	blocked, blocker := store.TicketID(input.TicketID), store.TicketID(input.BlockerTicketID)
	if blocked == blocker {
		return nil, clientError(http.StatusBadRequest, "self_dependency", "a Ticket cannot block itself")
	}
	if _, err := service.ticket(ctx, blocked); err != nil {
		return nil, ticketStoreError(err)
	}
	if _, err := service.ticket(ctx, blocker); err != nil {
		return nil, ticketStoreError(err)
	}
	path, err := service.tickets.AddTicketDependencyIfAcyclic(ctx, blocker, blocked)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	if len(path) > 0 {
		return nil, clientError(http.StatusConflict, "cycle", "dependency would create cycle "+ticketPathString(append(path, blocked)))
	}
	return &struct{}{}, nil
}

func (service *Service) removeBlocker(ctx context.Context, input *ticketPathInput) (*struct{}, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	if err := service.tickets.RemoveTicketDependency(ctx, store.TicketID(input.BlockerTicketID), store.TicketID(input.TicketID)); err != nil {
		return nil, ticketStoreError(err)
	}
	return &struct{}{}, nil
}

func (service *Service) listTickets(ctx context.Context, input *listTicketsInput) (*ticketsOutput, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	var tickets []store.Ticket
	var err error
	if !input.State.Valid() {
		tickets, err = service.tickets.Tickets(ctx)
	} else {
		tickets, err = service.tickets.TicketsByState(ctx, input.State)
	}
	if err != nil {
		return nil, ticketStoreError(err)
	}
	ready, err := service.tickets.ReadyTickets(ctx)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	readySet := make(map[store.TicketID]bool, len(ready))
	for _, ticket := range ready {
		readySet[ticket.ID] = true
	}
	var readyFilter *bool
	if input.Ready != "" {
		parsed, parseErr := strconv.ParseBool(input.Ready)
		if parseErr != nil {
			return nil, clientError(http.StatusBadRequest, "invalid_ready", "ready must be true or false")
		}
		readyFilter = &parsed
	}
	output := &ticketsOutput{}
	for _, ticket := range tickets {
		isReady := readySet[ticket.ID]
		if readyFilter == nil || *readyFilter == isReady {
			output.Body.Tickets = append(output.Body.Tickets, ticketSummaryFrom(ticket, isReady))
		}
	}
	return output, nil
}

func (service *Service) ticketOutput(ctx context.Context, ticket store.Ticket) (*ticketOutput, error) {
	blockers, err := service.tickets.TicketBlockers(ctx, ticket.ID)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	blocks, err := service.tickets.TicketBlocks(ctx, ticket.ID)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	ready, err := service.tickets.ReadyTickets(ctx)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	readySet := make(map[store.TicketID]bool, len(ready))
	for _, candidate := range ready {
		readySet[candidate.ID] = true
	}
	isReady := readySet[ticket.ID]
	response := ticketResponse{ID: int64(ticket.ID), Title: ticket.Title, Body: ticket.Body, State: ticket.State.String(), Ready: isReady, CreatedAt: wireTime(ticket.CreatedAt), UpdatedAt: wireTime(ticket.UpdatedAt)}
	for _, blocker := range blockers {
		response.Blockers = append(response.Blockers, ticketSummaryFrom(blocker, readySet[blocker.ID]))
	}
	for _, blocked := range blocks {
		response.Blocks = append(response.Blocks, ticketSummaryFrom(blocked, readySet[blocked.ID]))
	}
	return &ticketOutput{Body: response}, nil
}

func (service *Service) ticket(ctx context.Context, id store.TicketID) (store.Ticket, error) {
	return service.tickets.Ticket(ctx, id)
}
func wireTime(value time.Time) string { return value.UTC().Format(time.RFC3339) }
func ticketSummaryFrom(ticket store.Ticket, ready bool) ticketSummary {
	return ticketSummary{ID: int64(ticket.ID), Title: ticket.Title, State: ticket.State.String(), Ready: ready, CreatedAt: wireTime(ticket.CreatedAt), UpdatedAt: wireTime(ticket.UpdatedAt)}
}

func ticketPathString(path []store.TicketID) string {
	values := make([]string, 0, len(path))
	for _, id := range path {
		values = append(values, strconv.FormatInt(int64(id), 10))
	}
	return strings.Join(values, " -> ")
}

func clientError(status int, reason, message string) error {
	return &ticketError{ErrorModel: huma.ErrorModel{Status: status, Title: http.StatusText(status), Detail: message}, Reason: reason}
}

func ticketStoreError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return clientError(http.StatusNotFound, "not_found", "Ticket was not found")
	}
	return clientError(http.StatusInternalServerError, "internal", "ticket store operation failed")
}

func commandOperation(summary, description string) func(*huma.Operation) {
	return func(operation *huma.Operation) {
		operation.Summary = summary
		operation.Description = description
	}
}

// pause waits for the Dispatcher to acknowledge and apply the policy update.
func (service *Service) pause(ctx context.Context, _ *struct{}) (*struct{}, error) {
	paused := true
	return service.updateConfig(ctx, work.ConfigUpdate{Paused: &paused})
}

// resume waits for the Dispatcher to acknowledge and apply the policy update.
func (service *Service) resume(ctx context.Context, _ *struct{}) (*struct{}, error) {
	paused := false
	return service.updateConfig(ctx, work.ConfigUpdate{Paused: &paused})
}

// setMaxInFlight waits for the Dispatcher to acknowledge and apply the policy update.
func (service *Service) setMaxInFlight(ctx context.Context, input *maxInFlightInput) (*struct{}, error) {
	return service.updateConfig(ctx, work.ConfigUpdate{MaxInFlight: &input.Body.MaxInFlight})
}

// workNow waits for the Dispatcher to acknowledge an immediate re-evaluation.
func (service *Service) workNow(ctx context.Context, input *ticketInput) (*struct{}, error) {
	if service.commands == nil {
		return nil, clientError(http.StatusServiceUnavailable, "commands_unavailable", "factory commands are not configured")
	}
	if err := service.commands.WorkNow(ctx, input.TicketID); err != nil {
		return nil, commandError(err)
	}
	return &struct{}{}, nil
}

// cancelTicket accepts cancellation of the ticket workflow; it does not wait
// for terminal cleanup or any database state to become observable.
func (service *Service) cancelTicket(ctx context.Context, input *ticketInput) (*struct{}, error) {
	if service.commands == nil {
		return nil, clientError(http.StatusServiceUnavailable, "commands_unavailable", "factory commands are not configured")
	}
	if err := service.commands.CancelTicket(ctx, input.TicketID); err != nil {
		return nil, commandError(err)
	}
	return &struct{}{}, nil
}

func (service *Service) updateConfig(ctx context.Context, update work.ConfigUpdate) (*struct{}, error) {
	if service.commands == nil {
		return nil, clientError(http.StatusServiceUnavailable, "commands_unavailable", "factory commands are not configured")
	}
	if err := service.commands.UpdateConfig(ctx, update); err != nil {
		return nil, commandError(err)
	}
	return &struct{}{}, nil
}

func commandError(err error) error {
	switch {
	case errors.Is(err, work.ErrWorkflowNotFound):
		return clientError(http.StatusNotFound, "workflow_not_found", "workflow does not exist")
	case errors.Is(err, work.ErrWorkflowClosed):
		return clientError(http.StatusConflict, "workflow_closed", "workflow is already closed")
	default:
		return clientError(http.StatusServiceUnavailable, "unavailable", "Temporal is temporarily unavailable")
	}
}

// Handler serves the typed API routes and Huma's generated OpenAPI documents.
func (s *Service) Handler() http.Handler { return s.handler }

// OpenAPIYAML returns the generated OpenAPI 3.1 document without starting HTTP.
func (s *Service) OpenAPIYAML() ([]byte, error) { return s.api.OpenAPI().YAML() }
