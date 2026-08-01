package sf

import (
	"context"
	"fmt"
)

// Actions are high-level operations shared between CLI and TUI.
type Actions struct {
	Client *APIClient
}

// ConsoleStatus is a small summary for `sf factory status`.
type ConsoleStatus struct {
	Total    int
	Open     int
	Active   int
	Done     int
	Failed   int
	Ready    int
	NotReady int
}

// Status collects a live snapshot view from the API and derives counts.
func (actions *Actions) Status(ctx context.Context) (ConsoleStatus, error) {
	tickets, err := actions.Client.Console(ctx)
	if err != nil {
		return ConsoleStatus{}, err
	}
	var status ConsoleStatus
	status.Total = len(tickets)
	for _, ticket := range tickets {
		switch ticket.State {
		case "open":
			status.Open++
		case "active":
			status.Active++
		case "done":
			status.Done++
		case "failed":
			status.Failed++
		}
		if ticket.Ready {
			status.Ready++
		} else {
			status.NotReady++
		}
	}
	return status, nil
}

// ListTickets lists tickets for the current context.
func (actions *Actions) ListTickets(ctx context.Context, state string, ready string) ([]TicketSummary, error) {
	return actions.Client.ListTickets(ctx, state, ready)
}

// ConsoleTickets lists all tickets from the console snapshot endpoint.
func (actions *Actions) ConsoleTickets(ctx context.Context) ([]TicketSummary, error) {
	return actions.Client.Console(ctx)
}

// GetTicket returns one ticket.
func (actions *Actions) GetTicket(ctx context.Context, ticketID int64) (TicketResponse, error) {
	return actions.Client.GetTicket(ctx, ticketID)
}

// CreateTicket creates one ticket.
func (actions *Actions) CreateTicket(ctx context.Context, title, body string, blockedBy []int64) (TicketResponse, error) {
	return actions.Client.CreateTicket(ctx, title, body, blockedBy)
}

// SetTicketState updates one ticket state.
func (actions *Actions) SetTicketState(ctx context.Context, ticketID int64, state string) (TicketResponse, error) {
	return actions.Client.UpdateTicketState(ctx, ticketID, state)
}

// AddBlocker sets one blocker edge.
func (actions *Actions) AddBlocker(ctx context.Context, ticketID, blockerID int64) error {
	return actions.Client.SetBlocker(ctx, ticketID, blockerID, false)
}

// RemoveBlocker removes one blocker edge.
func (actions *Actions) RemoveBlocker(ctx context.Context, ticketID, blockerID int64) error {
	return actions.Client.SetBlocker(ctx, ticketID, blockerID, true)
}

// WorkTicket requests immediate scheduling.
func (actions *Actions) WorkTicket(ctx context.Context, ticketID int64) error {
	return actions.Client.WorkTicket(ctx, ticketID)
}

// CancelTicket requests cancellation.
func (actions *Actions) CancelTicket(ctx context.Context, ticketID int64) error {
	return actions.Client.CancelTicket(ctx, ticketID)
}

// PauseFactory pauses the dispatcher.
func (actions *Actions) PauseFactory(ctx context.Context) error {
	return actions.Client.PauseFactory(ctx)
}

// ResumeFactory resumes the dispatcher.
func (actions *Actions) ResumeFactory(ctx context.Context) error {
	return actions.Client.ResumeFactory(ctx)
}

// GetRun returns one run by id.
func (actions *Actions) GetRun(ctx context.Context, ticketID int64, runID string) (RunOutput, error) {
	return actions.Client.GetRun(ctx, ticketID, runID)
}

// SetMaxInFlight updates dispatch limit.
func (actions *Actions) SetMaxInFlight(ctx context.Context, maxInFlight int) error {
	return actions.Client.SetMaxInFlight(ctx, maxInFlight)
}

// ListTicketRuns lists all runs for ticket.
func (actions *Actions) ListTicketRuns(ctx context.Context, ticketID int64) ([]RunOutput, error) {
	return actions.Client.ListRuns(ctx, ticketID)
}

// BuildVersion returns version.
func (actions *Actions) BuildVersion(ctx context.Context) (string, error) {
	var raw struct {
		Version string `json:"version"`
	}
	if err := actions.Client.do(ctx, "GET", "/v1/build", nil, &raw); err != nil {
		return "", err
	}
	return raw.Version, nil
}

// FormatSummary returns a compact line for CLI and TUI status rendering.
func FormatSummary(status ConsoleStatus) string {
	return fmt.Sprintf("total=%d open=%d active=%d done=%d failed=%d ready=%d not-ready=%d", status.Total, status.Open, status.Active, status.Done, status.Failed, status.Ready, status.NotReady)
}
