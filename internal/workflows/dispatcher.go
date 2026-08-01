package workflows

import (
	"fmt"
	"sort"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// UpdateDispatcherPolicy is the acknowledged update used to publish a worker's
// complete resolved policy before it begins polling the main task queue.
const UpdateDispatcherPolicy = "publish-dispatcher-policy"

// UpdateDispatcherWorkNow is the acknowledged request to cancel the current
// long poll and immediately re-evaluate dispatchable work.
const UpdateDispatcherWorkNow = "dispatch-ticket-work-now"

// QueryDispatcherPolicy returns the current acknowledged target policy.
const QueryDispatcherPolicy = "target-dispatcher-policy"

// maxPolicyUpdatesDuringDrain keeps a suggested execution bounded while still
// letting one later worker publication become the policy of the next run.
const maxPolicyUpdatesDuringDrain = 1

// ticketActs resolves target dispatcher database activities by registered name.
var ticketActs *activities.TicketActivities

// DispatcherInput is the durable target-dispatcher state. Live child Futures
// never cross a Continue-As-New boundary: the workflow drains first.
type DispatcherInput struct {
	Policy   work.DispatcherPolicy
	CloneURL string
	Model    work.Model
}

// DispatcherPublication is the durable outcome of a policy publication.
type DispatcherPublication string

const (
	// DispatcherPublicationApplied means a different resolved policy became current.
	DispatcherPublicationApplied DispatcherPublication = "APPLIED"
	// DispatcherPublicationAlreadyCurrent means the fingerprint matched current policy.
	DispatcherPublicationAlreadyCurrent DispatcherPublication = "ALREADY_CURRENT"
	// DispatcherPublicationDraining tells callers to retry after the rollover.
	DispatcherPublicationDraining DispatcherPublication = "DRAINING"
)

// DispatcherPolicyUpdate is one idempotently named policy publication. Request
// identity belongs to Temporal's Update ID, not to this payload.
type DispatcherPolicyUpdate struct {
	Fingerprint string
	Policy      work.DispatcherPolicy
}

// DispatcherPolicyStatus is the observable target control state.
type DispatcherPolicyStatus struct {
	Policy      work.DispatcherPolicy
	Fingerprint string
	Draining    bool
}

// DispatcherWorkNowRequest preserves the ticket-specific API request in
// Temporal history even though the fresh poll re-evaluates all dispatchable
// Tickets under the current policy.
type DispatcherWorkNowRequest struct {
	TicketID int64
}

// DispatcherWorkNowAcknowledgement is the durable outcome of a wake request.
type DispatcherWorkNowAcknowledgement string

const (
	// DispatcherWorkNowAcknowledged means the Dispatcher accepted the request
	// and scheduled an immediate re-evaluation.
	DispatcherWorkNowAcknowledged DispatcherWorkNowAcknowledgement = "ACKNOWLEDGED"
	// DispatcherWorkNowDraining means the current execution cannot start a new
	// poll because it is rolling over.
	DispatcherWorkNowDraining DispatcherWorkNowAcknowledgement = "DRAINING"
)

// Dispatcher admits target WorkOnTicket children. Idle polling is represented
// by the retry state of AwaitDispatchableTickets, not workflow timers.
func Dispatcher(ctx workflow.Context, in DispatcherInput) error {
	if err := validateDispatcherInput(in); err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), activities.ErrTypeInvalid, nil)
	}
	policy := in.Policy
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), activities.ErrTypeInvalid, nil)
	}
	var wait workflow.Future
	var cancelWait workflow.CancelFunc
	wakeRequested := workflow.NewBufferedChannel(ctx, 1)
	draining := false
	drainPolicyUpdates := 0
	if err := workflow.SetQueryHandler(ctx, QueryDispatcherPolicy, func() (DispatcherPolicyStatus, error) {
		return DispatcherPolicyStatus{Policy: policy, Fingerprint: fingerprint, Draining: draining}, nil
	}); err != nil {
		return fmt.Errorf("registering dispatcher policy query: %w", err)
	}
	beginDraining := func() {
		if draining {
			return
		}
		draining = true
		if cancelWait != nil {
			cancelWait()
			wait, cancelWait = nil, nil
		}
	}
	if err := workflow.SetUpdateHandler(ctx, UpdateDispatcherPolicy, func(_ workflow.Context, update DispatcherPolicyUpdate) (DispatcherPublication, error) {
		next, err := update.Policy.Fingerprint()
		if err != nil || update.Fingerprint != next {
			return "", temporal.NewNonRetryableApplicationError("published dispatcher policy fingerprint is invalid", activities.ErrTypeInvalid, err)
		}
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			beginDraining()
		}
		if update.Fingerprint == fingerprint {
			return DispatcherPublicationAlreadyCurrent, nil
		}
		if draining && drainPolicyUpdates >= maxPolicyUpdatesDuringDrain {
			return DispatcherPublicationDraining, nil
		}
		policy, fingerprint = update.Policy, update.Fingerprint
		if wakeRequested.Len() == 0 {
			wakeRequested.Send(ctx, struct{}{})
		}
		if draining {
			drainPolicyUpdates++
		}
		return DispatcherPublicationApplied, nil
	}); err != nil {
		return fmt.Errorf("registering dispatcher policy update: %w", err)
	}
	if err := workflow.SetUpdateHandler(ctx, UpdateDispatcherWorkNow, func(_ workflow.Context, request DispatcherWorkNowRequest) (DispatcherWorkNowAcknowledgement, error) {
		if request.TicketID < 1 {
			return "", temporal.NewNonRetryableApplicationError("work-now ticket ID must be positive", activities.ErrTypeInvalid, nil)
		}
		if draining {
			return DispatcherWorkNowDraining, nil
		}
		if wakeRequested.Len() == 0 {
			wakeRequested.Send(ctx, struct{}{})
		}
		return DispatcherWorkNowAcknowledged, nil
	}); err != nil {
		return fmt.Errorf("registering dispatcher work-now update: %w", err)
	}

	children := map[store.TicketID]workflow.ChildWorkflowFuture{}
	for {
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			beginDraining()
		}
		if draining && len(children) == 0 {
			return workflow.NewContinueAsNewError(ctx, Dispatcher, DispatcherInput{Policy: policy, CloneURL: in.CloneURL, Model: in.Model})
		}
		if !draining && !policy.Paused && wait == nil && len(children) < policy.MaxInFlight {
			waitCtx, cancel := workflow.WithCancel(ctx)
			wait = workflow.ExecuteActivity(workflow.WithActivityOptions(waitCtx, dispatchWaitActivityOptions(policy.Run.Recording)), ticketActs.AwaitDispatchableTickets)
			cancelWait = cancel
		}

		selector := workflow.NewSelector(ctx)
		if wait != nil {
			selector.AddFuture(wait, func(f workflow.Future) {
				if cancelWait != nil {
					cancelWait()
				}
				wait, cancelWait = nil, nil
				var tickets []store.Ticket
				if err := f.Get(ctx, &tickets); err != nil {
					return
				}
				for _, ticket := range sortedDispatchableTickets(tickets) {
					if len(children) >= policy.MaxInFlight {
						return
					}
					if _, exists := children[ticket.ID]; exists {
						continue
					}
					childOptions := dispatchChildWorkflowOptions(ticket.ID, policy.Run)
					child := workflow.ExecuteChildWorkflow(workflow.WithChildOptions(ctx, childOptions), WorkOnTicket, WorkOnTicketInput{TicketID: ticket.ID, Policy: policy.Run, CloneURL: in.CloneURL, Model: in.Model})
					children[ticket.ID] = child
				}
			})
		}
		for _, id := range sortedChildTicketIDs(children) {
			child := children[id]
			id, child := id, child
			selector.AddFuture(child, func(workflow.Future) { delete(children, id) })
		}
		selector.AddReceive(wakeRequested, func(channel workflow.ReceiveChannel, _ bool) {
			var ignored struct{}
			channel.Receive(ctx, &ignored)
			if cancelWait != nil {
				cancelWait()
			}
			wait, cancelWait = nil, nil
		})
		selector.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {})
		selector.Select(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func dispatchChildWorkflowOptions(ticketID store.TicketID, policy work.TargetRunPolicy) workflow.ChildWorkflowOptions {
	return workflow.ChildWorkflowOptions{
		WorkflowID:               work.TicketWorkflowID(int64(ticketID)),
		TaskQueue:                work.TaskQueue,
		WorkflowExecutionTimeout: policy.HardDeadline,
		ParentClosePolicy:        enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
	}
}

// dispatchWaitActivityOptions deliberately omits ScheduleToCloseTimeout: an
// idle dispatcher is allowed to wait forever in one retrying activity. The
// activity's no-work error controls the ordinary cadence with NextRetryDelay;
// technical failures retain the resolved infrastructure retry policy.
func dispatchWaitActivityOptions(policy work.ActivityPolicy) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: policy.StartToCloseTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    policy.Retry.InitialInterval,
			BackoffCoefficient: policy.Retry.BackoffCoefficient,
			MaximumInterval:    policy.Retry.MaximumInterval,
			MaximumAttempts:    0,
		},
	}
}

func validateDispatcherInput(in DispatcherInput) error {
	if err := in.Policy.Validate(); err != nil {
		return err
	}
	if in.CloneURL == "" || in.Model.Name == "" {
		return fmt.Errorf("dispatcher requires a repository URL and model")
	}
	return nil
}

func sortedDispatchableTickets(tickets []store.Ticket) []store.Ticket {
	sorted := append([]store.Ticket(nil), tickets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return sorted
}

func sortedChildTicketIDs(children map[store.TicketID]workflow.ChildWorkflowFuture) []store.TicketID {
	ids := make([]store.TicketID, 0, len(children))
	for id := range children {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
