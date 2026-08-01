package workflows

import (
	"errors"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/work"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestRemainingSessionExecutionTimeoutUsesOneAbsoluteDeadline(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	first, err := remainingSessionExecutionTimeout(deadline.Add(-20*time.Hour), deadline)
	if err != nil || first != 20*time.Hour {
		t.Fatalf("initial remaining timeout = %s, %v; want 20h", first, err)
	}
	replacement, err := remainingSessionExecutionTimeout(deadline.Add(-time.Hour), deadline)
	if err != nil || replacement != time.Hour {
		t.Fatalf("replacement remaining timeout = %s, %v; want 1h from the original deadline", replacement, err)
	}

	_, err = remainingSessionExecutionTimeout(deadline, deadline)
	var application *temporal.ApplicationError
	if !errors.As(err, &application) || application.Type() != activities.ErrTypeHardDeadline {
		t.Fatalf("elapsed deadline error = %v, want typed %q", err, activities.ErrTypeHardDeadline)
	}
}

func TestAgentContinueAsNewCarriesOnlyThePolicyItsHistoryNeeds(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		unbounded bool
	}{
		{name: "legacy replay retains legacy wire fields"},
		{name: "unbounded execution drops legacy wire fields", unbounded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			legacy := defaultLegacyAgentLimits()
			input := AgentWorkflowInput{LegacyLimits: legacy}
			state := AgentWorkflowState{ConversationRef: agent.ConversationRef{Key: "conversation"}}
			suite := &testsuite.WorkflowTestSuite{}
			environment := suite.NewTestWorkflowEnvironment()
			environment.ExecuteWorkflow(func(ctx workflow.Context) error {
				return continueAgentWorkflowAsNew(ctx, input, state, test.unbounded)
			})
			var continued *workflow.ContinueAsNewError
			if !errors.As(environment.GetWorkflowError(), &continued) {
				t.Fatalf("workflow error = %v, want ContinueAsNew", environment.GetWorkflowError())
			}
			var next AgentWorkflowInput
			if err := converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next); err != nil {
				t.Fatalf("decode continued input: %v", err)
			}
			if test.unbounded && next.LegacyLimits != nil {
				t.Fatalf("unbounded continuation retained legacy limits: %+v", next.LegacyLimits)
			}
			if !test.unbounded && (next.LegacyLimits == nil || *next.LegacyLimits != *legacy) {
				t.Fatalf("legacy continuation limits = %+v, want %+v", next.LegacyLimits, legacy)
			}
		})
	}
}

func TestTargetFailureFreshAttemptPolicyReplacesOnlyUnrecoverableExecutions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		kind agent.TerminalFailureKind
		want bool
	}{
		{kind: agent.TerminalFailureSessionLost, want: false},
		{kind: agent.TerminalFailureAmbiguousToolExecution, want: true},
		{kind: agent.TerminalFailureInvalidProviderOutcome, want: true},
		{kind: agent.TerminalFailureModelExhausted, want: false},
		{kind: agent.TerminalFailureRateLimited, want: false},
		{kind: agent.TerminalFailureAuthentication, want: false},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			failure := &agent.TerminalFailure{Kind: test.kind}
			if got := targetFailureNeedsFreshAttempt(failure); got != test.want {
				t.Fatalf("targetFailureNeedsFreshAttempt(%s) = %t, want %t", test.kind, got, test.want)
			}
		})
	}
}

func TestTargetAgentChildOptionsAllowSameAttemptRecovery(t *testing.T) {
	t.Parallel()
	policy := work.DefaultTargetRunPolicy().Agent
	options := targetAgentChildOptions("agent/run-1/step/5/attempt/1", policy)
	if options.WorkflowID != "agent/run-1/step/5/attempt/1" || options.WorkflowExecutionTimeout != policy.ScheduleToCloseTimeout ||
		options.WorkflowIDReusePolicy != enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE ||
		!options.WaitForCancellation || options.ParentClosePolicy != enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL {
		t.Fatalf("target child options = %#v", options)
	}
}

func TestWorkOnTicketExecutionTimeoutExposesThePolicyHardDeadline(t *testing.T) {
	t.Parallel()
	policy := work.DefaultTargetRunPolicy()
	policy.HardDeadline = 30 * time.Hour
	if got := WorkOnTicketExecutionTimeout(policy); got != 30*time.Hour {
		t.Fatalf("execution timeout = %s, want 30h", got)
	}
}
