package workflows_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

var ticketActs *activities.TicketActivities

var _ = Describe("Dispatcher", func() {
	It("rolls over only from Temporal's suggestion and carries no live state", func() {
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
		Expect(errors.As(env.GetWorkflowError(), &continued)).To(BeTrue(), "Dispatcher should continue as new")
		var next workflows.DispatcherInput
		Expect(converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next)).NotTo(HaveOccurred(), "decoding continue-as-new input")
		Expect(next.CloneURL).To(Equal(in.CloneURL))
		Expect(next.Model).To(Equal(in.Model))
		got, err := next.Policy.Fingerprint()
		Expect(err).NotTo(HaveOccurred(), "fingerprinting continued policy")
		want, err := in.Policy.Fingerprint()
		Expect(err).NotTo(HaveOccurred(), "fingerprinting input policy")
		Expect(got).To(Equal(want), "policy fingerprints should be preserved")
	})

	It("publishes the latest accepted policy", func() {
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
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-first", dispatcherUpdateCallback("first", updates, errs), mustDispatchPolicyUpdate(first))
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-second", dispatcherUpdateCallback("second", updates, errs), mustDispatchPolicyUpdate(second))
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-second", dispatcherUpdateCallback("second-retry", updates, errs), mustDispatchPolicyUpdate(second))
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "publication-second-comparison", dispatcherUpdateCallback("second-comparison", updates, errs), mustDispatchPolicyUpdate(second))
		}, 0)
		env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
		env.ExecuteWorkflow(workflows.Dispatcher, in)

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel after policy assertions")
		for name, err := range errs {
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("%s publication", name))
		}
		Expect(updates["first"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(updates["second"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(updates["second-retry"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(updates["second-comparison"]).To(Equal(workflows.DispatcherPublicationAlreadyCurrent))
	})

	It("admits tickets up to capacity with policy snapshots", func() {
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

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel after admission assertions")
		Expect(awaits).To(Equal(1), "AwaitDispatchableTickets calls")
		Expect(admitted).To(HaveLen(2), "admitted children")
		admittedIDs := map[store.TicketID]bool{}
		for _, child := range admitted {
			admittedIDs[child.TicketID] = true
		}
		Expect(admittedIDs[3]).To(BeTrue())
		Expect(admittedIDs[5]).To(BeTrue())
		Expect(admittedIDs[9]).NotTo(BeTrue())
		for _, child := range admitted {
			Expect(reflect.DeepEqual(child.Policy, in.Policy.Run)).To(BeTrue(), "ticket policies should snapshot the input policy")
		}
	})

	It("starts a real child with its temporal run ID", func() {
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

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel after child assertion")
		Expect(childRunID).NotTo(BeEmpty(), "real WorkOnTicket child should start")
		Expect(claimed.RunID).To(Equal(childRunID), "claimed run ID must match child run ID")
	})

	It("does not poll until an unpaused policy is published", func() {
		suite := &testsuite.WorkflowTestSuite{}
		env := suite.NewTestWorkflowEnvironment()
		polls := 0
		unpauseRequested := false
		env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).
			Return(func(context.Context) ([]store.Ticket, error) {
				polls++
				if !unpauseRequested {
					Fail("dispatcher polled before an unpaused policy was published")
				}
				return []store.Ticket{{ID: 17, Title: "admit after unpause", State: store.TicketOpen}}, nil
			})
		env.OnWorkflow(workflows.WorkOnTicket, mock.Anything, mock.Anything).
			Return(func(ctx workflow.Context, _ workflows.WorkOnTicketInput) error {
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
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "unpause", dispatcherUpdateCallback("unpause", updates, errs), mustDispatchPolicyUpdate(unpaused))
		}, 10*time.Second)
		env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
		env.ExecuteWorkflow(workflows.Dispatcher, in)

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel after unpause assertion")
		Expect(errs["unpause"]).NotTo(HaveOccurred(), "unpause publication")
		Expect(updates["unpause"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(polls).To(Equal(1), "dispatch polls after unpause")
	})

	It("cancels an outstanding wait when paused", func() {
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
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "pause", dispatcherUpdateCallback("pause", updates, errs), mustDispatchPolicyUpdate(paused))
		}, time.Second)
		canceledBeforeWorkflowStop := false
		env.RegisterDelayedCallback(func() {
			canceledBeforeWorkflowStop = canceled
			env.CancelWorkflow()
		}, 2*time.Second)
		env.ExecuteWorkflow(workflows.Dispatcher, in)

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel after assertion")
		Expect(errs["pause"]).NotTo(HaveOccurred(), "pause publication")
		Expect(updates["pause"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(canceledBeforeWorkflowStop).To(BeTrue())
	})

	It("cancels outstanding wait for work-now and starts a fresh one", func() {
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

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel after assertion")
		Expect(updateErr).NotTo(HaveOccurred(), "work-now update")
		Expect(acknowledgement).To(Equal(workflows.DispatcherWorkNowAcknowledged))
		Expect(pollsBeforeWorkflowStop).To(Equal(2), "dispatch polls before workflow stop")
	})

	It("releases a completed child slot without ContinueAsNew", func() {
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

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel")
		Expect(polls).To(BeNumerically(">=", 2), "dispatch polls after child completion")
	})

	It("uses the latest published policy for later admissions", func() {
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
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "latest-before-admission", dispatcherUpdateCallback("latest", updates, errs), mustDispatchPolicyUpdate(latest))
		}, 0)
		env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Second)
		env.ExecuteWorkflow(workflows.Dispatcher, in)

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel")
		Expect(errs["latest"]).NotTo(HaveOccurred(), "latest publication")
		Expect(updates["latest"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(reflect.DeepEqual(admitted.Policy, latest.Run)).To(BeTrue(), "admitted policy")
	})

	It("gives each child its admitted hard deadline", func() {
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
			Expect(args.Get(&input)).NotTo(HaveOccurred(), "decoding child input")
			timeouts[input.TicketID] = info.WorkflowExecutionTimeout
			inputs[input.TicketID] = input
		})

		updates := map[string]workflows.DispatcherPublication{}
		errs := map[string]error{}
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "later-hard-deadline", dispatcherUpdateCallback("later", updates, errs), mustDispatchPolicyUpdate(later))
		}, time.Second)
		env.RegisterDelayedCallback(env.CancelWorkflow, 10*time.Second)
		env.ExecuteWorkflow(workflows.Dispatcher, in)

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel")
		Expect(errs["later"]).NotTo(HaveOccurred(), "later publication")
		Expect(updates["later"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(timeouts[31]).To(Equal(firstDeadline))
		Expect(timeouts[32]).To(Equal(later.Run.HardDeadline))
		Expect(inputs[31].Policy.HardDeadline).To(Equal(firstDeadline))
		Expect(inputs[32].Policy.HardDeadline).To(Equal(later.Run.HardDeadline))
	})

	It("routes WorkOnTicket children to the main queue", func() {
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

		Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue(), "dispatcher should cancel after child option assertions")
		Expect(childTaskQueue).To(Equal(work.TaskQueue))
		Expect(childExecutionTimeout).To(Equal(in.Policy.Run.HardDeadline))
	})

	It("cancels an outstanding wait before draining", func() {
		suite := &testsuite.WorkflowTestSuite{}
		env := suite.NewTestWorkflowEnvironment()
		env.OnActivity(ticketActs.AwaitDispatchableTickets, mock.Anything).After(time.Hour).Return([]store.Ticket{}, nil)
		updates := map[string]workflows.DispatcherPublication{}
		errs := map[string]error{}
		env.RegisterDelayedCallback(func() {
			env.SetContinueAsNewSuggested(true)
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "drain-wait", dispatcherUpdateCallback("drain-wait", updates, errs), mustDispatchPolicyUpdate(work.DefaultDispatcherPolicy()))
		}, time.Second)
		// A wait that was not canceled would still be outstanding here and this
		// cancellation would win instead of the required empty-state rollover.
		env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Second)
		env.ExecuteWorkflow(workflows.Dispatcher, targetDispatcherInput())

		var continued *workflow.ContinueAsNewError
		Expect(errors.As(env.GetWorkflowError(), &continued)).To(BeTrue(), "dispatcher should continue as new after canceling wait")
		Expect(errs["drain-wait"]).NotTo(HaveOccurred(), "drain publication")
	})

	It("drains tracked children before ContinueAsNew", func() {
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
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "accepted-during-drain", dispatcherUpdateCallback("accepted", updates, errs), mustDispatchPolicyUpdate(accepted))
		}, 10*time.Second)
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "rejected-during-drain", dispatcherUpdateCallback("rejected", updates, errs), mustDispatchPolicyUpdate(rejected))
		}, 20*time.Second)
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(workflows.UpdateDispatcherPolicy, "duplicate-during-drain", dispatcherUpdateCallback("duplicate", updates, errs), mustDispatchPolicyUpdate(accepted))
		}, 30*time.Second)
		env.ExecuteWorkflow(workflows.Dispatcher, in)

		var continued *workflow.ContinueAsNewError
		Expect(errors.As(env.GetWorkflowError(), &continued)).To(BeTrue(), "dispatcher should continue as new")
		Expect(awaits).To(Equal(1), "AwaitDispatchableTickets calls")
		for name, err := range errs {
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("%s drain publication", name))
		}
		Expect(updates["accepted"]).To(Equal(workflows.DispatcherPublicationApplied))
		Expect(updates["rejected"]).To(Equal(workflows.DispatcherPublicationDraining))
		Expect(updates["duplicate"]).To(Equal(workflows.DispatcherPublicationAlreadyCurrent))
		var next workflows.DispatcherInput
		Expect(converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next)).NotTo(HaveOccurred(), "decoding continued dispatcher input")
		got, err := next.Policy.Fingerprint()
		Expect(err).NotTo(HaveOccurred(), "fingerprinting continued policy")
		want, err := accepted.Fingerprint()
		Expect(err).NotTo(HaveOccurred(), "fingerprinting accepted policy")
		Expect(got).To(Equal(want))
	})
})

func targetDispatcherInput() workflows.DispatcherInput {
	return workflows.DispatcherInput{
		Policy:   work.DefaultDispatcherPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	}
}

func mustDispatchPolicyUpdate(policy work.DispatcherPolicy) workflows.DispatcherPolicyUpdate {
	update, err := targetDispatcherPolicyUpdate(policy)
	if err != nil {
		Fail(fmt.Sprintf("fingerprinting policy: %v", err))
		return workflows.DispatcherPolicyUpdate{}
	}
	return update
}

func targetDispatcherPolicyUpdate(policy work.DispatcherPolicy) (workflows.DispatcherPolicyUpdate, error) {
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return workflows.DispatcherPolicyUpdate{}, err
	}
	return workflows.DispatcherPolicyUpdate{Fingerprint: fingerprint, Policy: policy}, nil
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
