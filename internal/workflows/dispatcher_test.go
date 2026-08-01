package workflows_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	"github.com/stretchr/testify/mock"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

var ticketActs *activities.TicketActivities

func TestDispatcherRollsOverOnlyFromTemporalsSuggestionAndCarriesNoLiveState(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.SetContinueAsNewSuggested(true)
	in := workflows.DispatcherInput{
		Policy:   work.DefaultDispatcherPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	}
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	var continued *workflow.ContinueAsNewError
	if err := env.GetWorkflowError(); !errors.As(err, &continued) {
		t.Fatalf("Dispatcher error = %v, want ContinueAsNewError", err)
	}
	var next workflows.DispatcherInput
	if err := converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next); err != nil {
		t.Fatalf("decoding continue-as-new input: %v", err)
	}
	if next.CloneURL != in.CloneURL || next.Model != in.Model {
		t.Fatalf("continued dispatcher input = %+v, want source configuration retained", next)
	}
	got, err := next.Policy.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprinting continued policy: %v", err)
	}
	want, err := in.Policy.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprinting input policy: %v", err)
	}
	if got != want {
		t.Fatalf("continued policy fingerprint = %q, want %q", got, want)
	}
}

func TestDispatcherPublishesTheLatestAcceptedPolicy(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return([]store.Ticket{}, temporal.NewApplicationError("no dispatchable tickets", activities.ErrTypeNoDispatchableTickets, nil))

	in := targetDispatcherInput()
	first := in.Policy
	first.MaxInFlight = 2
	second := in.Policy
	second.MaxInFlight = 3
	updates := map[string]workflows.DispatcherPublication{}
	errs := map[string]error{}
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-first", dispatcherUpdateCallback("first", updates, errs), targetDispatcherPolicyUpdate(t, first))
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-second", dispatcherUpdateCallback("second", updates, errs), targetDispatcherPolicyUpdate(t, second))
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-second", dispatcherUpdateCallback("second-retry", updates, errs), targetDispatcherPolicyUpdate(t, second))
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-second-comparison", dispatcherUpdateCallback("second-comparison", updates, errs), targetDispatcherPolicyUpdate(t, second))
	}, 0)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation after update assertions", err)
	}
	for name, err := range errs {
		if err != nil {
			t.Errorf("%s publication failed: %v", name, err)
		}
	}
	if got := updates["first"]; got != workflows.DispatcherPublicationApplied {
		t.Errorf("first publication = %q, want APPLIED", got)
	}
	if got := updates["second"]; got != workflows.DispatcherPublicationApplied {
		t.Errorf("second publication = %q, want APPLIED", got)
	}
	if got := updates["second-retry"]; got != workflows.DispatcherPublicationApplied {
		t.Errorf("duplicate request publication = %q, want original APPLIED response", got)
	}
	if got := updates["second-comparison"]; got != workflows.DispatcherPublicationAlreadyCurrent {
		t.Errorf("repeated latest policy under a new request = %q, want ALREADY_CURRENT", got)
	}
}

func TestDispatcherAdmitsTicketsUpToCapacityWithPolicySnapshots(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	awaits := 0
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return(func(context.Context) ([]store.Ticket, error) {
			awaits++
			return []store.Ticket{
				{ID: 9, Title: "third", State: store.TicketOpen},
				{ID: 3, Title: "first", State: store.TicketOpen},
				{ID: 5, Title: "second", State: store.TicketOpen},
			}, nil
		})
	var admitted []workflows.WorkOnTicketInput
	env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).
		Return(func(ctx workflow.Context, in workflows.WorkOnTicketInput) error {
			admitted = append(admitted, in)
			return workflow.Sleep(ctx, time.Hour)
		})

	in := targetDispatcherInput()
	in.Policy.MaxInFlight = 2
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation after admission assertions", err)
	}
	if awaits != 1 {
		t.Errorf("AwaitDispatchableTickets calls = %d, want exactly one while capacity is full", awaits)
	}
	if len(admitted) != 2 {
		t.Fatalf("admitted %d children, want capacity 2", len(admitted))
	}
	admittedIDs := map[store.TicketID]bool{}
	for _, child := range admitted {
		admittedIDs[child.TicketID] = true
	}
	if !admittedIDs[3] || !admittedIDs[5] || admittedIDs[9] {
		t.Errorf("admitted Ticket IDs = %v, want the two sorted IDs before capacity", admittedIDs)
	}
	for _, child := range admitted {
		if !reflect.DeepEqual(child.Policy, in.Policy.Run) {
			t.Errorf("ticket %d policy = %+v, want input snapshot %+v", child.TicketID, child.Policy, in.Policy.Run)
		}
	}
}

func TestDispatcherRealChildClaimsWithItsTemporalRunID(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.WorkOnTicket)

	var claimed store.ClaimRunInput
	env.RegisterActivityWithOptions(
		func(_ context.Context, input store.ClaimRunInput) (store.ClaimRunResult, error) {
			claimed = input
			return store.ClaimRunResult{}, temporal.NewNonRetryableApplicationError("captured authoritative run ID", activities.ErrTypeInvalid, nil)
		},
		sdkactivity.RegisterOptions{Name: "ClaimAndStartRun"},
	)

	waits := 0
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return(func(context.Context) ([]store.Ticket, error) {
			waits++
			if waits == 1 {
				return []store.Ticket{{ID: 17, Title: "derive child run identity", State: store.TicketOpen}}, nil
			}
			return nil, temporal.NewApplicationError("no dispatchable tickets", activities.ErrTypeNoDispatchableTickets)
		})

	var childRunID string
	env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		childRunID = info.WorkflowExecution.RunID
	})
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(workflows.Dispatcher, targetDispatcherInput())

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation after child assertion", err)
	}
	if childRunID == "" {
		t.Fatal("real WorkOnTicket child did not start")
	}
	if claimed.RunID != childRunID {
		t.Fatalf("claimed Run ID = %q, want child Temporal Run ID %q", claimed.RunID, childRunID)
	}
}

func TestDispatcherDoesNotPollUntilAnUnpausedPolicyIsPublished(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	polls := 0
	unpauseRequested := false
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return(func(context.Context) ([]store.Ticket, error) {
			polls++
			if !unpauseRequested {
				t.Error("dispatcher polled before an unpaused policy was published")
			}
			return []store.Ticket{{ID: 17, Title: "admit after unpause", State: store.TicketOpen}}, nil
		})
	env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).Return(func(ctx workflow.Context, _ workflows.WorkOnTicketInput) error {
		return workflow.Sleep(ctx, time.Hour)
	})

	in := targetDispatcherInput()
	in.Policy.Paused = true
	unpaused := in.Policy
	unpaused.Paused = false
	updates := map[string]workflows.DispatcherPublication{}
	errs := map[string]error{}
	env.RegisterDelayedCallback(func() {
		unpauseRequested = true
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "unpause", dispatcherUpdateCallback("unpause", updates, errs), targetDispatcherPolicyUpdate(t, unpaused))
	}, 10*time.Second)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation after the unpause assertion", err)
	}
	if err := errs["unpause"]; err != nil {
		t.Fatalf("unpause publication failed: %v", err)
	}
	if updates["unpause"] != workflows.DispatcherPublicationApplied {
		t.Errorf("unpause publication = %q, want APPLIED", updates["unpause"])
	}
	if polls != 1 {
		t.Errorf("dispatch polls = %d, want exactly one after unpausing", polls)
	}
}

func TestDispatcherCancelsAnOutstandingPollWhenPaused(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	canceled := false
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).After(time.Hour).Return([]store.Ticket{}, nil)
	env.SetOnActivityCanceledListener(func(info *sdkactivity.Info) {
		if info.ActivityType.Name == "AwaitDispatchableTickets" {
			canceled = true
		}
	})

	in := targetDispatcherInput()
	paused := in.Policy
	paused.Paused = true
	updates := map[string]workflows.DispatcherPublication{}
	errs := map[string]error{}
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "pause", dispatcherUpdateCallback("pause", updates, errs), targetDispatcherPolicyUpdate(t, paused))
	}, time.Second)
	canceledBeforeWorkflowStop := false
	env.RegisterDelayedCallback(func() {
		canceledBeforeWorkflowStop = canceled
		env.CancelWorkflow()
	}, 2*time.Second)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation after assertion", err)
	}
	if err := errs["pause"]; err != nil {
		t.Fatalf("pause publication failed: %v", err)
	}
	if updates["pause"] != workflows.DispatcherPublicationApplied {
		t.Errorf("pause publication = %q, want APPLIED", updates["pause"])
	}
	if !canceledBeforeWorkflowStop {
		t.Fatal("pause left the outstanding dispatch poll active")
	}
}

func TestDispatcherWorkNowCancelsTheOutstandingPollAndStartsAFreshOne(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	polls := 0
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).After(time.Hour).Return([]store.Ticket{}, nil)
	env.SetOnActivityStartedListener(func(info *sdkactivity.Info, _ context.Context, _ converter.EncodedValues) {
		if info.ActivityType.Name == "AwaitDispatchableTickets" {
			polls++
		}
	})

	var acknowledgement workflows.DispatcherWorkNowAcknowledgement
	var updateErr error
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(workflows.UpdateDispatcherWorkNow, "work-now-42", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { updateErr = err },
			OnAccept: func() {},
			OnComplete: func(value interface{}, err error) {
				updateErr = err
				if err == nil {
					acknowledgement = value.(workflows.DispatcherWorkNowAcknowledgement)
				}
			},
		}, workflows.DispatcherWorkNowRequest{TicketID: 42})
	}, time.Second)
	pollsBeforeWorkflowStop := 0
	env.RegisterDelayedCallback(func() {
		pollsBeforeWorkflowStop = polls
		env.CancelWorkflow()
	}, 2*time.Second)
	env.ExecuteWorkflow(workflows.Dispatcher, targetDispatcherInput())

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation after assertion", err)
	}
	if updateErr != nil {
		t.Fatalf("work-now update failed: %v", updateErr)
	}
	if acknowledgement != workflows.DispatcherWorkNowAcknowledged {
		t.Errorf("work-now acknowledgement = %q, want ACKNOWLEDGED", acknowledgement)
	}
	if pollsBeforeWorkflowStop != 2 {
		t.Errorf("dispatch polls before workflow stop = %d, want initial and fresh polls", pollsBeforeWorkflowStop)
	}
}

func TestDispatcherReleasesACompletedChildSlotWithoutContinueAsNew(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	polls := 0
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return(func(context.Context) ([]store.Ticket, error) {
			polls++
			if polls == 1 {
				return []store.Ticket{{ID: 23, Title: "finish me", State: store.TicketOpen}}, nil
			}
			return nil, temporal.NewApplicationError("no dispatchable tickets", activities.ErrTypeNoDispatchableTickets, nil)
		})
	env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).Return(nil)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(workflows.Dispatcher, targetDispatcherInput())

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation", err)
	}
	if polls < 2 {
		t.Errorf("dispatch polls = %d, want a second wait after the child released its only slot", polls)
	}
}

func TestDispatcherUsesTheLatestPublishedPolicyForLaterAdmissions(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).After(time.Second).
		Return([]store.Ticket{{ID: 31, Title: "admit with latest policy", State: store.TicketOpen}}, nil)
	var admitted workflows.WorkOnTicketInput
	env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).
		Return(func(ctx workflow.Context, in workflows.WorkOnTicketInput) error {
			admitted = in
			return workflow.Sleep(ctx, time.Hour)
		})

	in := targetDispatcherInput()
	latest := in.Policy
	latest.MaxInFlight = 2
	updates := map[string]workflows.DispatcherPublication{}
	errs := map[string]error{}
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "latest-before-admission", dispatcherUpdateCallback("latest", updates, errs), targetDispatcherPolicyUpdate(t, latest))
	}, 0)
	env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Second)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation", err)
	}
	if err := errs["latest"]; err != nil {
		t.Fatalf("latest publication failed: %v", err)
	}
	if updates["latest"] != workflows.DispatcherPublicationApplied {
		t.Errorf("latest publication = %q, want APPLIED", updates["latest"])
	}
	if !reflect.DeepEqual(admitted.Policy, latest.Run) {
		t.Errorf("admitted policy = %+v, want latest published policy %+v", admitted.Policy, latest.Run)
	}
}

func TestDispatcherGivesEachChildItsAdmittedHardDeadline(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	polls := 0
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return(func(context.Context) ([]store.Ticket, error) {
			polls++
			switch polls {
			case 1:
				return []store.Ticket{{ID: 31, Title: "first policy", State: store.TicketOpen}}, nil
			case 2:
				return []store.Ticket{{ID: 32, Title: "later policy", State: store.TicketOpen}}, nil
			default:
				return nil, temporal.NewApplicationError("no dispatchable tickets", activities.ErrTypeNoDispatchableTickets, nil)
			}
		})
	env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).
		Return(func(ctx workflow.Context, in workflows.WorkOnTicketInput) error {
			if in.TicketID == 31 {
				return workflow.Sleep(ctx, 5*time.Second)
			}
			return workflow.Sleep(ctx, time.Hour)
		})

	in := targetDispatcherInput()
	firstDeadline := in.Policy.Run.HardDeadline
	later := in.Policy
	later.Run.SemanticDeadline = 47 * time.Hour
	later.Run.HardDeadline = 48 * time.Hour

	timeouts := map[store.TicketID]time.Duration{}
	inputs := map[store.TicketID]workflows.WorkOnTicketInput{}
	env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, args converter.EncodedValues) {
		var input workflows.WorkOnTicketInput
		if err := args.Get(&input); err != nil {
			t.Errorf("decoding child input: %v", err)
			return
		}
		timeouts[input.TicketID] = info.WorkflowExecutionTimeout
		inputs[input.TicketID] = input
	})

	updates := map[string]workflows.DispatcherPublication{}
	errs := map[string]error{}
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "later-hard-deadline", dispatcherUpdateCallback("later", updates, errs), targetDispatcherPolicyUpdate(t, later))
	}, time.Second)
	env.RegisterDelayedCallback(env.CancelWorkflow, 10*time.Second)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation", err)
	}
	if err := errs["later"]; err != nil {
		t.Fatalf("later publication failed: %v", err)
	}
	if updates["later"] != workflows.DispatcherPublicationApplied {
		t.Errorf("later publication = %q, want APPLIED", updates["later"])
	}
	if got := timeouts[31]; got != firstDeadline {
		t.Errorf("first child WorkflowExecutionTimeout = %s, want its admitted hard deadline %s", got, firstDeadline)
	}
	if got := timeouts[32]; got != later.Run.HardDeadline {
		t.Errorf("later child WorkflowExecutionTimeout = %s, want later admitted hard deadline %s", got, later.Run.HardDeadline)
	}
	if got := inputs[31].Policy.HardDeadline; got != firstDeadline {
		t.Errorf("first child policy hard deadline = %s, want immutable admitted deadline %s", got, firstDeadline)
	}
	if got := inputs[32].Policy.HardDeadline; got != later.Run.HardDeadline {
		t.Errorf("later child policy hard deadline = %s, want later admitted deadline %s", got, later.Run.HardDeadline)
	}
}

func TestDispatcherRoutesWorkOnTicketChildrenToTheMainRunQueue(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return([]store.Ticket{{ID: 41, Title: "run on main", State: store.TicketOpen}}, nil)
	var childTaskQueue string
	var childExecutionTimeout time.Duration
	env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		childTaskQueue = info.TaskQueueName
		childExecutionTimeout = info.WorkflowExecutionTimeout
	})
	env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).
		Return(func(ctx workflow.Context, _ workflows.WorkOnTicketInput) error {
			return workflow.Sleep(ctx, time.Hour)
		})

	in := targetDispatcherInput()
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("Dispatcher error = %v, want cancellation after child option assertions", err)
	}
	if childTaskQueue != work.TaskQueue {
		t.Errorf("child task queue = %q, want main run queue %q", childTaskQueue, work.TaskQueue)
	}
	if childExecutionTimeout != in.Policy.Run.HardDeadline {
		t.Errorf("child WorkflowExecutionTimeout = %s, want immutable policy hard deadline %s", childExecutionTimeout, in.Policy.Run.HardDeadline)
	}
}

func TestDispatcherCancelsAnOutstandingWaitBeforeDraining(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).After(time.Hour).Return([]store.Ticket{}, nil)
	updates := map[string]workflows.DispatcherPublication{}
	errs := map[string]error{}
	env.RegisterDelayedCallback(func() {
		env.SetContinueAsNewSuggested(true)
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "drain-wait", dispatcherUpdateCallback("drain-wait", updates, errs), targetDispatcherPolicyUpdate(t, work.DefaultDispatcherPolicy()))
	}, time.Second)
	// A wait that was not canceled would still be outstanding here and this
	// cancellation would win instead of the required empty-state rollover.
	env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Second)
	env.ExecuteWorkflow(workflows.Dispatcher, targetDispatcherInput())

	var continued *workflow.ContinueAsNewError
	if err := env.GetWorkflowError(); !errors.As(err, &continued) {
		t.Fatalf("Dispatcher error = %v, want ContinueAsNewError after canceling its wait", err)
	}
	if err := errs["drain-wait"]; err != nil {
		t.Fatalf("drain publication failed: %v", err)
	}
}

func TestDispatcherDrainsTrackedChildrenBeforeContinuingAsNew(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	awaits := 0
	env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
		Return(func(context.Context) ([]store.Ticket, error) {
			awaits++
			return []store.Ticket{{ID: 42, Title: "dispatch me", State: store.TicketOpen}}, nil
		})
	env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).
		Return(func(ctx workflow.Context, _ workflows.WorkOnTicketInput) error {
			return workflow.Sleep(ctx, time.Minute)
		})

	in := targetDispatcherInput()
	accepted := in.Policy
	accepted.MaxInFlight = 2
	rejected := in.Policy
	rejected.MaxInFlight = 3
	updates := map[string]workflows.DispatcherPublication{}
	errs := map[string]error{}
	env.RegisterDelayedCallback(func() {
		env.SetContinueAsNewSuggested(true)
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "accepted-during-drain", dispatcherUpdateCallback("accepted", updates, errs), targetDispatcherPolicyUpdate(t, accepted))
	}, 10*time.Second)
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "rejected-during-drain", dispatcherUpdateCallback("rejected", updates, errs), targetDispatcherPolicyUpdate(t, rejected))
	}, 20*time.Second)
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "duplicate-during-drain", dispatcherUpdateCallback("duplicate", updates, errs), targetDispatcherPolicyUpdate(t, accepted))
	}, 30*time.Second)
	env.ExecuteWorkflow(workflows.Dispatcher, in)

	var continued *workflow.ContinueAsNewError
	if err := env.GetWorkflowError(); !errors.As(err, &continued) {
		t.Fatalf("Dispatcher error = %v, want ContinueAsNewError", err)
	}
	if awaits != 1 {
		t.Errorf("AwaitDispatchableTickets calls = %d, want exactly one before drain", awaits)
	}
	for name, err := range errs {
		if err != nil {
			t.Errorf("%s drain publication failed: %v", name, err)
		}
	}
	if got := updates["accepted"]; got != workflows.DispatcherPublicationApplied {
		t.Errorf("first policy during drain = %q, want APPLIED", got)
	}
	if got := updates["rejected"]; got != workflows.DispatcherPublicationDraining {
		t.Errorf("second policy during drain = %q, want DRAINING", got)
	}
	if got := updates["duplicate"]; got != workflows.DispatcherPublicationAlreadyCurrent {
		t.Errorf("duplicate policy during drain = %q, want ALREADY_CURRENT", got)
	}
	var next workflows.DispatcherInput
	if err := converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next); err != nil {
		t.Fatalf("decoding continued dispatcher input: %v", err)
	}
	if got, err := next.Policy.Fingerprint(); err != nil {
		t.Fatalf("fingerprinting continued policy: %v", err)
	} else if want, err := accepted.Fingerprint(); err != nil {
		t.Fatalf("fingerprinting accepted policy: %v", err)
	} else if got != want {
		t.Errorf("continued policy = %q, want last accepted %q", got, want)
	}
}

func targetDispatcherInput() workflows.DispatcherInput {
	return workflows.DispatcherInput{
		Policy:   work.DefaultDispatcherPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	}
}

func targetDispatcherPolicyUpdate(t *testing.T, policy work.DispatcherPolicy) workflows.DispatcherPolicyUpdate {
	t.Helper()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprinting policy: %v", err)
	}
	return workflows.DispatcherPolicyUpdate{Fingerprint: fingerprint, Policy: policy}
}

func dispatcherUpdateCallback(name string, updates map[string]workflows.DispatcherPublication, errs map[string]error) *testsuite.TestUpdateCallback {
	return &testsuite.TestUpdateCallback{
		OnReject: func(err error) { errs[name] = err },
		OnAccept: func() {},
		OnComplete: func(value interface{}, err error) {
			if err != nil {
				errs[name] = err
				return
			}
			publication, ok := value.(workflows.DispatcherPublication)
			if !ok {
				errs[name] = fmt.Errorf("unexpected publication type %T", value)
				return
			}
			updates[name] = publication
		},
	}
}
