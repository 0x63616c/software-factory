// Package temporal seals target Dispatcher commands behind factory types.
package temporal

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

type commandClient interface {
	QueryWorkflow(context.Context, string, string, string, ...interface{}) (converter.EncodedValue, error)
	UpdateWorkflow(context.Context, client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error)
	CancelWorkflow(context.Context, string, string) error
}

// Commands sends acknowledged target control commands to Temporal.
type Commands struct{ client commandClient }

// NewCommands wraps a live Temporal client. The composition root owns its lifetime.
func NewCommands(temporal client.Client) *Commands { return &Commands{client: temporal} }

// UpdateConfig applies the supported operational patch to the current target
// policy, then waits for the stable Dispatcher to acknowledge it.
func (commands *Commands) UpdateConfig(ctx context.Context, update work.ConfigUpdate) error {
	encoded, err := commands.client.QueryWorkflow(ctx, work.TargetDispatcherWorkflowID, "", workflows.QueryDispatcherPolicy)
	if err != nil {
		return classify("query target dispatcher policy", err)
	}
	var status workflows.DispatcherPolicyStatus
	if err := encoded.Get(&status); err != nil {
		return fmt.Errorf("decode target dispatcher policy: %w", err)
	}
	policy := status.Policy
	if update.Paused != nil {
		policy.Paused = *update.Paused
	}
	if update.MaxInFlight != nil {
		policy.MaxInFlight = *update.MaxInFlight
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("validate target dispatcher policy update: %w", err)
	}
	handle, err := commands.client.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   work.TargetDispatcherWorkflowID,
		UpdateName:   workflows.UpdateDispatcherPolicy,
		UpdateID:     "api-" + uuid.NewString(),
		Args:         []interface{}{workflows.DispatcherPolicyUpdate{Fingerprint: fingerprint, Policy: policy}},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return classify("update target dispatcher policy", err)
	}
	var outcome workflows.DispatcherPublication
	if err := handle.Get(ctx, &outcome); err != nil {
		return classify("wait for target dispatcher policy", err)
	}
	if outcome == workflows.DispatcherPublicationDraining {
		return fmt.Errorf("target dispatcher is draining: %w", work.ErrWorkflowClosed)
	}
	return nil
}

// WorkNow asks the target Dispatcher to cancel its current long poll and
// immediately re-evaluate dispatchable work, preserving the requested Ticket
// in Temporal history.
func (commands *Commands) WorkNow(ctx context.Context, ticketID int) error {
	if ticketID < 1 {
		return fmt.Errorf("work-now ticket ID must be positive")
	}
	handle, err := commands.client.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   work.TargetDispatcherWorkflowID,
		UpdateName:   workflows.UpdateDispatcherWorkNow,
		UpdateID:     "api-work-now-" + uuid.NewString(),
		Args:         []interface{}{workflows.DispatcherWorkNowRequest{TicketID: int64(ticketID)}},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return classify("request immediate target dispatch", err)
	}
	var acknowledgement workflows.DispatcherWorkNowAcknowledgement
	if err := handle.Get(ctx, &acknowledgement); err != nil {
		return classify("wait for immediate target dispatch", err)
	}
	if acknowledgement == workflows.DispatcherWorkNowDraining {
		return fmt.Errorf("target dispatcher is draining: %w", work.ErrWorkflowClosed)
	}
	if acknowledgement != workflows.DispatcherWorkNowAcknowledged {
		return fmt.Errorf("unexpected target work-now acknowledgement %q", acknowledgement)
	}
	return nil
}

// CancelTicket requests cancellation of the target WorkOnTicket execution.
func (commands *Commands) CancelTicket(ctx context.Context, ticketID int) error {
	err := commands.client.CancelWorkflow(ctx, work.TicketWorkflowID(int64(ticketID)), "")
	if err != nil {
		return classify(fmt.Sprintf("cancel target ticket %d", ticketID), err)
	}
	return nil
}

func classify(operation string, err error) error {
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return fmt.Errorf("%s: %w", operation, work.ErrWorkflowNotFound)
	}
	var failedPrecondition *serviceerror.FailedPrecondition
	if errors.As(err, &failedPrecondition) {
		return fmt.Errorf("%s: %w", operation, work.ErrWorkflowClosed)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
