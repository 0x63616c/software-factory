// Package sf provides a small typed client for software-factory API endpoints.
package sf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIClient is the HTTP client used by `sf` CLI/TUI command actions.
type APIClient struct {
	httpClient *http.Client
	baseURL    *url.URL
	authHeader string
	authToken  string
}

// Credentials holds auth information used by every request.
type Credentials struct {
	CfAccessToken string
	BearerToken   string
}

// NewClient constructs an authenticated API client for the active context.
func NewClient(baseURL string, credentials Credentials, timeout time.Duration, httpClient *http.Client) (*APIClient, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parsing api-url %q: %w", baseURL, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("api-url must include a host: %q", baseURL)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	httpClient.Timeout = timeout

	client := &APIClient{
		httpClient: httpClient,
		baseURL:    parsed,
	}
	client.authHeader = "Authorization"
	client.authToken = strings.TrimSpace(credentials.BearerToken)
	if strings.TrimSpace(credentials.CfAccessToken) != "" {
		client.authHeader = "Cf-Access-Jwt-Assertion"
		client.authToken = strings.TrimSpace(credentials.CfAccessToken)
	}
	return client, nil
}

// BuildEndpoint joins and returns one safe endpoint path.
func (client *APIClient) BuildEndpoint(path string) string {
	parsed, err := url.Parse(path)
	if err != nil {
		// Fall back to prior path-only behavior if a malformed path is passed.
		return client.baseURL.ResolveReference(&url.URL{Path: path}).String()
	}
	return client.baseURL.ResolveReference(parsed).String()
}

func (client *APIClient) doRaw(ctx context.Context, method, endpoint string, payload any) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("marshalling %s %s: %w", method, endpoint, err)
		}
		body = bytes.NewReader(raw)
	}

	request, err := http.NewRequestWithContext(ctx, method, client.BuildEndpoint(endpoint), body)
	if err != nil {
		return nil, 0, fmt.Errorf("building %s request %s: %w", method, endpoint, err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.authToken != "" {
		if client.authHeader == "Cf-Access-Jwt-Assertion" {
			request.Header.Set(client.authHeader, client.authToken)
		} else {
			request.Header.Set(client.authHeader, "Bearer "+client.authToken)
		}
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("sending %s request %s: %w", method, endpoint, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s response for %s: %w", endpoint, method, err)
	}

	if response.StatusCode/100 != 2 {
		apiErr := parseErrorResponse(response.StatusCode, raw)
		return nil, response.StatusCode, apiErr
	}
	if len(raw) == 0 {
		return nil, response.StatusCode, nil
	}
	return raw, response.StatusCode, nil
}

func (client *APIClient) do(ctx context.Context, method, endpoint string, payload any, out any) error {
	raw, status, err := client.doRaw(ctx, method, endpoint, payload)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return APIError{
			Status: status,
			Reason: "invalid_json",
			Detail: fmt.Sprintf("decode %s response: %v", endpoint, err),
		}
	}
	return nil
}

// ListTickets returns every ticket matching the provided optional filter.
func (client *APIClient) ListTickets(ctx context.Context, state string, ready string) ([]TicketSummary, error) {
	path := "/v1/tickets"
	if state != "" || ready != "" {
		query := make([]string, 0, 2)
		if state != "" {
			query = append(query, "state="+url.QueryEscape(state))
		}
		if ready != "" {
			query = append(query, "ready="+url.QueryEscape(ready))
		}
		path = path + "?" + strings.Join(query, "&")
	}
	var body struct {
		Tickets []TicketSummary `json:"tickets"`
	}
	if err := client.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return nil, err
	}
	return body.Tickets, nil
}

// Console returns the current console snapshot.
func (client *APIClient) Console(ctx context.Context) ([]TicketSummary, error) {
	var body struct {
		Tickets []TicketSummary `json:"tickets"`
	}
	if err := client.do(ctx, http.MethodGet, "/v1/console", nil, &body); err != nil {
		return nil, err
	}
	return body.Tickets, nil
}

// GetTicket returns one ticket detail.
func (client *APIClient) GetTicket(ctx context.Context, ticketID int64) (TicketResponse, error) {
	var ticket TicketResponse
	path := fmt.Sprintf("/v1/tickets/%d", ticketID)
	if err := client.do(ctx, http.MethodGet, path, nil, &ticket); err != nil {
		return TicketResponse{}, err
	}
	return ticket, nil
}

// CreateTicket creates a new ticket.
func (client *APIClient) CreateTicket(ctx context.Context, title, body string, blockers []int64) (TicketResponse, error) {
	payload := struct {
		Title     string  `json:"title"`
		Body      string  `json:"body"`
		BlockedBy []int64 `json:"blockedBy"`
	}{
		Title:     title,
		Body:      body,
		BlockedBy: blockers,
	}
	var ticket TicketResponse
	if err := client.do(ctx, http.MethodPost, "/v1/tickets", payload, &ticket); err != nil {
		return TicketResponse{}, err
	}
	return ticket, nil
}

// UpdateTicketState patches ticket state.
func (client *APIClient) UpdateTicketState(ctx context.Context, ticketID int64, state string) (TicketResponse, error) {
	payload := struct {
		State string `json:"state"`
	}{
		State: state,
	}
	var ticket TicketResponse
	path := fmt.Sprintf("/v1/tickets/%d/state", ticketID)
	if err := client.do(ctx, http.MethodPatch, path, payload, &ticket); err != nil {
		return TicketResponse{}, err
	}
	return ticket, nil
}

// SetBlocker sets or removes an edge between tickets.
func (client *APIClient) SetBlocker(ctx context.Context, ticketID, blockerID int64, remove bool) error {
	method := http.MethodPut
	if remove {
		method = http.MethodDelete
	}
	path := fmt.Sprintf("/v1/tickets/%d/blockers/%d", ticketID, blockerID)
	return client.do(ctx, method, path, nil, nil)
}

// WorkTicket requests immediate work now for one ticket.
func (client *APIClient) WorkTicket(ctx context.Context, ticketID int64) error {
	path := fmt.Sprintf("/v1/tickets/%d/work", ticketID)
	return client.do(ctx, http.MethodPost, path, nil, nil)
}

// CancelTicket sends the cancellation command for a ticket.
func (client *APIClient) CancelTicket(ctx context.Context, ticketID int64) error {
	path := fmt.Sprintf("/v1/tickets/%d/cancel", ticketID)
	return client.do(ctx, http.MethodPost, path, nil, nil)
}

// PauseFactory pauses dispatching.
func (client *APIClient) PauseFactory(ctx context.Context) error {
	return client.do(ctx, http.MethodPost, "/v1/factory/pause", nil, nil)
}

// ResumeFactory resumes dispatching.
func (client *APIClient) ResumeFactory(ctx context.Context) error {
	return client.do(ctx, http.MethodPost, "/v1/factory/resume", nil, nil)
}

// SetMaxInFlight sets the max open concurrent runs.
func (client *APIClient) SetMaxInFlight(ctx context.Context, maxInFlight int) error {
	var body struct {
		MaxInFlight int `json:"maxInFlight"`
	}
	body.MaxInFlight = maxInFlight
	return client.do(ctx, http.MethodPost, "/v1/factory/max-in-flight", body, nil)
}

// ListRuns returns all runs for one ticket.
func (client *APIClient) ListRuns(ctx context.Context, ticketID int64) ([]RunOutput, error) {
	var body struct {
		Runs []RunOutput `json:"runs"`
	}
	path := fmt.Sprintf("/v1/tickets/%d/runs", ticketID)
	if err := client.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return nil, err
	}
	return body.Runs, nil
}

// GetRun returns one run on one ticket.
func (client *APIClient) GetRun(ctx context.Context, ticketID int64, runID string) (RunOutput, error) {
	runs, err := client.ListRuns(ctx, ticketID)
	if err != nil {
		return RunOutput{}, err
	}
	for _, run := range runs {
		if run.ID == runID {
			return run, nil
		}
	}
	return RunOutput{}, APIError{
		Status: http.StatusNotFound,
		Reason: "not_found",
		Detail: "run not found for ticket",
	}
}

// GetTranscript downloads one transcript body.
func (client *APIClient) GetTranscript(ctx context.Context, ticketID int64, runID string, ordinal, attempt int) ([]byte, error) {
	path := fmt.Sprintf("/v1/tickets/%d/runs/%s/steps/%d/attempts/%d/transcript", ticketID, runID, ordinal, attempt)
	raw, _, err := client.doRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		var apiErr APIError
		if errors.As(err, &apiErr) {
			return nil, apiErr
		}
		return nil, err
	}
	return raw, nil
}

// APIError is the typed API error used across CLI and TUI.
type APIError struct {
	Status int
	Reason string
	Detail string
	Title  string
	Type   string
}

func (err APIError) Error() string {
	return err.Detail
}

func parseErrorResponse(status int, body []byte) error {
	var response ErrorResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &response); err == nil && response.Reason != "" {
			return APIError{
				Status: response.Status,
				Reason: response.Reason,
				Detail: response.Detail,
				Title:  response.Title,
				Type:   response.Type,
			}
		}
	}
	switch status {
	case http.StatusUnauthorized:
		return APIError{Status: status, Reason: "unauthorized", Detail: "unauthorized"}
	case http.StatusNotFound:
		return APIError{Status: status, Reason: "not_found", Detail: http.StatusText(status)}
	case http.StatusConflict:
		return APIError{Status: status, Reason: "conflict", Detail: http.StatusText(status)}
	case http.StatusServiceUnavailable:
		return APIError{Status: status, Reason: "unavailable", Detail: http.StatusText(status)}
	default:
		return APIError{Status: status, Reason: "internal", Detail: http.StatusText(status)}
	}
}

// ExitCode translates stable API reasons into stable exit semantics.
func ExitCode(err error) int {
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		return 1
	}
	switch apiErr.Reason {
	case "not_found":
		return 12
	case "conflict", "illegal_transition", "workflow_closed", "self_dependency", "invalid_ready", "cycle":
		return 10
	case "unauthorized", "workflow_not_found", "internal":
		return 11
	case "invalid_request":
		return 13
	case "unavailable":
		return 14
	case "commands_unavailable", "store_unavailable", "bad_request":
		return 15
	default:
		return 1
	}
}
