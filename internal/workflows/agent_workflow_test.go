package workflows_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const testToolsetFingerprint = "sha256:test-toolset"

const testAgentRunID = "019fb900-0000-7000-8000-000000000001"

func TestAgentWorkflowCompletesFromOneFinalModelTurn(t *testing.T) {
	t.Parallel()

	conversationRef := agent.ConversationRef{Key: "conversations/agent/run-7/plan/0/digest", Revision: 0, Bytes: 100, Digest: "digest"}
	textRef := agent.TextRef{Key: "conversations/agent/run-7/plan/artifacts/text/final", Bytes: 18, Digest: "final"}
	expected := work.NewStageOutput(work.StagePlan, work.DocumentOutput{Document: "the plan"})
	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	var lifecycle []agentactivities.LifecycleInput
	registerAgentLifecycle(environment, &lifecycle)
	environment.RegisterActivityWithOptions(
		func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
			return agentactivities.PrepareOutput{ConversationRef: conversationRef}, nil
		}, activity.RegisterOptions{Name: agent.PrepareActivityName},
	)
	environment.RegisterActivityWithOptions(
		func(_ context.Context, input agent.ModelTurnInput) (agent.ModelTurnResult, error) {
			if input.ConversationRef != conversationRef || input.ModelTurn != 1 {
				t.Fatalf("model input = %#v", input)
			}
			return agent.ModelTurnResult{
				Outcome: agent.OutcomeFinalText, ToolsetFingerprint: testToolsetFingerprint,
				ConversationRef: conversationRef, FinalTextRef: textRef,
				Usage: work.Usage{InputTokens: 10, OutputTokens: 3}, UsageMeasured: true,
			}, nil
		}, activity.RegisterOptions{Name: agent.ModelTurnActivityName},
	)
	environment.RegisterActivityWithOptions(
		func(_ context.Context, input agentactivities.FinalizeInput) (agentactivities.FinalizeOutput, error) {
			if input.Stage != work.StagePlan || input.TextRef != textRef {
				t.Fatalf("finalize input = %#v", input)
			}
			return agentactivities.FinalizeOutput{Result: &expected}, nil
		}, activity.RegisterOptions{Name: agent.FinalizeActivityName},
	)
	input := workflows.AgentWorkflowInput{
		Attempt: activities.StageAttempt{
			Key:   work.StageKey{Ticket: 7, RunID: testAgentRunID, Stage: work.StagePlan, Turn: 1},
			Model: work.Model{Name: "gpt-test", Effort: "medium"},
		},
		ToolTarget: agent.ToolTarget{
			Kind:              agent.ToolTargetRunWorker,
			RunWorkerIdentity: work.RunWorkerIdentity{RunID: testAgentRunID, Generation: 1},
		},
		ToolsetID: "coding-read-v1", CacheKey: "run-7-plan",
		ModelTurnPolicy: work.DefaultTargetRunPolicy().Agent,
		ControlPolicy:   work.DefaultTargetRunPolicy().Recording,
	}
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult() error = %v", err)
	}
	if result.Failure != nil || result.Result.Prose() != "the plan" || result.ConversationRef != conversationRef || result.ModelTurns != 1 || result.ToolCalls != 0 ||
		result.Usage.InputTokens != 10 || !result.UsageMeasured {
		t.Fatalf("AgentWorkflow result = %#v", result)
	}
	if len(lifecycle) != 1 || lifecycle[0].Outcome != telemetry.AgentOutcomeSucceeded {
		t.Fatalf("lifecycle = %#v, want one success", lifecycle)
	}
}

func TestAgentWorkflowClassifiesAnInvalidProviderFinalizationWithoutAResult(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	conversation := agent.ConversationRef{Key: "conversations/agent/run-7/implement/1", Revision: 1, Bytes: 2}
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: conversation}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeFinalText, ToolsetFingerprint: testToolsetFingerprint,
			ConversationRef: conversation, FinalTextRef: agent.TextRef{Key: "text"}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.FinalizeInput) (agentactivities.FinalizeOutput, error) {
		return agentactivities.FinalizeOutput{}, temporal.NewNonRetryableApplicationError(
			"provider final output violates the expected schema", agent.ErrorTypeInvalidProviderOutcome, nil,
		)
	}, activity.RegisterOptions{Name: agent.FinalizeActivityName})

	environment.ExecuteWorkflow(workflows.AgentWorkflow, validAgentWorkflowInput(work.StageImplement))
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult() error = %v", err)
	}
	if result.Failure == nil || !result.Failure.Is(agent.TerminalFailureInvalidProviderOutcome) || result.Result.Stage() != "" {
		t.Fatalf("result = %#v, want only an invalid-provider terminal failure", result)
	}
}

func TestAgentWorkflowUsesTheSuppliedModelTurnPolicy(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	input := validAgentWorkflowInput(work.StageImplement)
	input.ModelTurnPolicy = work.DefaultTargetRunPolicy().Agent
	started := map[string]activity.Info{}
	environment.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		started[info.ActivityType.Name] = *info
	})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: agent.ConversationRef{Revision: 0, Bytes: 1}}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeFinalText, ToolsetFingerprint: testToolsetFingerprint,
			ConversationRef: agent.ConversationRef{Revision: 1, Bytes: 2}, FinalTextRef: agent.TextRef{Key: "text"}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.FinalizeInput) (agentactivities.FinalizeOutput, error) {
		return testAgentFinalizeOutput(work.NewStageOutput(work.StageImplement, work.ImplementOutput{Report: "done"})), nil
	}, activity.RegisterOptions{Name: agent.FinalizeActivityName})

	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	model, found := started[agent.ModelTurnActivityName]
	if !found {
		t.Fatalf("started activities = %v", started)
	}
	if model.StartToCloseTimeout != 55*time.Minute || model.ScheduleToCloseTimeout != 90*time.Minute || model.HeartbeatTimeout != 5*time.Minute {
		t.Fatalf("model activity timeouts = %#v", model)
	}
	options := workflows.AgentModelTurnActivityOptionsForTest(input.ModelTurnPolicy)
	if options.RetryPolicy == nil || options.RetryPolicy.InitialInterval != 10*time.Second || options.RetryPolicy.BackoffCoefficient != 2 ||
		options.RetryPolicy.MaximumInterval != 5*time.Minute || options.RetryPolicy.MaximumAttempts != 10 {
		t.Fatalf("model activity retry policy = %#v", options.RetryPolicy)
	}
	for _, name := range []string{agent.PrepareActivityName, agent.FinalizeActivityName} {
		control, found := started[name]
		if !found || control.StartToCloseTimeout != time.Minute || control.ScheduleToCloseTimeout != time.Hour || control.HeartbeatTimeout != 0 {
			t.Fatalf("%s control options = %#v", name, control)
		}
	}
}

func TestAgentWorkflowPassesAConversationSeedToPrepare(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	input := validAgentWorkflowInput(work.StageImplement)
	input.Attempt.Key.Turn = 2
	seed := &agent.ConversationSeed{
		Source:          work.StageKey{Ticket: 7, RunID: testAgentRunID, Stage: work.StageImplement, Turn: 1},
		SourceIdentity:  "agent/run-7/step/8/attempt/1",
		ConversationRef: agent.ConversationRef{Key: "conversations/agent/run-7/implement/1/0/digest", Bytes: 1, Digest: "digest"},
	}
	input.Seed = seed
	conversationRef := agent.ConversationRef{Key: "conversations/agent/run-7/implement/2/1/digest", Revision: 1, Bytes: 2, Digest: "digest"}
	environment.RegisterActivityWithOptions(func(_ context.Context, prepared agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		if prepared.Attempt.Key != input.Attempt.Key || prepared.Seed == nil || *prepared.Seed != *seed {
			t.Fatalf("Prepare input = %#v", prepared)
		}
		return agentactivities.PrepareOutput{ConversationRef: conversationRef}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	environment.RegisterActivityWithOptions(func(_ context.Context, turn agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeFinalText, ToolsetFingerprint: testToolsetFingerprint,
			ConversationRef: conversationRef, FinalTextRef: agent.TextRef{Key: "text"}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.FinalizeInput) (agentactivities.FinalizeOutput, error) {
		return testAgentFinalizeOutput(work.NewStageOutput(work.StageImplement, work.ImplementOutput{Report: "done"})), nil
	}, activity.RegisterOptions{Name: agent.FinalizeActivityName})

	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
}

func TestAgentWorkflowReturnsAmbiguousToolFailureWithPartialReferences(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	var lifecycle []agentactivities.LifecycleInput
	registerAgentLifecycle(environment, &lifecycle)
	environment.SetWorkerOptions(worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	initial := agent.ConversationRef{Key: "conversations/agent/run-7/implement/2/0/initial", Bytes: 10, Digest: "initial"}
	requested := agent.ConversationRef{Key: "conversations/agent/run-7/implement/2/1/requested", Revision: 1, Bytes: 20, Digest: "requested"}
	transcript := agent.TranscriptRef{Key: "conversations/agent/run-7/implement/2/transcript/1/event", Revision: 1, Bytes: 30, Digest: "event"}
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: initial}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeToolCalls, ToolsetFingerprint: testToolsetFingerprint, ConversationRef: requested, TranscriptRef: transcript,
			ToolCalls: []agent.PendingToolCall{{CallID: "call_1", Name: "read_file"}}, Usage: work.Usage{InputTokens: 11}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agent.ToolInput) (agent.ToolOutput, error) {
		return agent.ToolOutput{}, temporal.NewNonRetryableApplicationError("ambiguous", agent.ErrorTypeAmbiguousToolExecution, nil)
	}, activity.RegisterOptions{Name: agent.ToolActivityName})

	environment.ExecuteWorkflow(workflows.AgentWorkflow, validAgentWorkflowInput(work.StageImplement))
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult() error = %v", err)
	}
	if result.Failure == nil || !result.Failure.Is(agent.TerminalFailureAmbiguousToolExecution) ||
		result.Failure.Is(agent.TerminalFailureSessionLost) || result.ConversationRef != requested || result.TranscriptRef != transcript ||
		result.Usage != (work.Usage{InputTokens: 11}) || result.ModelTurns != 1 || result.ToolCalls != 0 {
		t.Fatalf("terminal result = %#v", result)
	}
	if len(lifecycle) != 1 || lifecycle[0].Outcome != telemetry.AgentOutcomeFailed || lifecycle[0].Budget != "" {
		t.Fatalf("lifecycle = %#v, want one non-budget failure", lifecycle)
	}
}

func TestAgentWorkflowResultWireContractFailsClosed(t *testing.T) {
	t.Parallel()

	success := work.NewStageOutput(work.StagePlan, work.DocumentOutput{Document: "the plan"})
	tests := []struct {
		name   string
		result workflows.AgentWorkflowResult
		valid  bool
	}{
		{name: "successful result", result: workflows.AgentWorkflowResult{Result: success}, valid: true},
		{name: "terminal failure", result: workflows.AgentWorkflowResult{Failure: &agent.TerminalFailure{Kind: agent.TerminalFailureSessionLost}}, valid: true},
		{name: "empty", result: workflows.AgentWorkflowResult{}},
		{name: "both outcomes", result: workflows.AgentWorkflowResult{Result: success, Failure: &agent.TerminalFailure{Kind: agent.TerminalFailureSessionLost}}},
		{name: "invalid failure", result: workflows.AgentWorkflowResult{Failure: &agent.TerminalFailure{Kind: "unknown"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.result)
			if (err == nil) != test.valid {
				t.Fatalf("Marshal() error = %v, want valid = %t", err, test.valid)
			}
			if !test.valid {
				return
			}
			var decoded workflows.AgentWorkflowResult
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
		})
	}

	for _, malformed := range []string{
		`{}`,
		`{"result":{"stage":"plan","value":{"document":"the plan"}},"failure":{"kind":"session_lost"}}`,
		`{"failure":{"kind":"budget_exhausted"}}`,
		`{"failure":{"kind":"session_lost","budget":"tool_calls"}}`,
	} {
		var decoded workflows.AgentWorkflowResult
		if err := json.Unmarshal([]byte(malformed), &decoded); err == nil {
			t.Fatalf("Unmarshal(%s) succeeded", malformed)
		}
	}
}

func TestAgentWorkflowDistinguishesSessionLossFromAmbiguousToolExecution(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	environment.SetWorkerOptions(worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: agent.ConversationRef{Revision: 0, Bytes: 10}}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		return agent.ModelTurnResult{Outcome: agent.OutcomeToolCalls, ToolsetFingerprint: testToolsetFingerprint, ConversationRef: agent.ConversationRef{Revision: 1, Bytes: 20}, ToolCalls: []agent.PendingToolCall{{CallID: "call_1", Name: "read_file"}}, UsageMeasured: true}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agent.ToolInput) (agent.ToolOutput, error) {
		return agent.ToolOutput{}, temporal.NewNonRetryableApplicationError("session lost", agent.ErrorTypeSessionLost, workflow.ErrSessionFailed)
	}, activity.RegisterOptions{Name: agent.ToolActivityName})

	environment.ExecuteWorkflow(workflows.AgentWorkflow, validAgentWorkflowInput(work.StageImplement))
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult() error = %v", err)
	}
	if result.Failure == nil || !result.Failure.Is(agent.TerminalFailureSessionLost) || result.Failure.Is(agent.TerminalFailureAmbiguousToolExecution) {
		t.Fatalf("terminal result = %#v", result)
	}
}

func TestAgentWorkflowClassifiesTerminalModelActivityFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want agent.TerminalFailureKind
	}{
		{name: "rate limited", err: temporal.NewNonRetryableApplicationError("capacity", agent.ErrorTypeRateLimit, nil), want: agent.TerminalFailureRateLimited},
		{name: "authentication", err: temporal.NewNonRetryableApplicationError("credential", agent.ErrorTypeAuth, nil), want: agent.TerminalFailureAuthentication},
		{name: "transient exhausted", err: temporal.NewApplicationError("temporary", agent.ErrorTypeTransient), want: agent.TerminalFailureModelExhausted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suite := &testsuite.WorkflowTestSuite{}
			environment := suite.NewTestWorkflowEnvironment()
			registerAgentLifecycle(environment, nil)
			environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
				return agentactivities.PrepareOutput{ConversationRef: agent.ConversationRef{Revision: 0, Bytes: 10}}, nil
			}, activity.RegisterOptions{Name: agent.PrepareActivityName})
			environment.RegisterActivityWithOptions(func(context.Context, agent.ModelTurnInput) (agent.ModelTurnResult, error) {
				return agent.ModelTurnResult{}, test.err
			}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
			input := validAgentWorkflowInput(work.StageImplement)
			input.ModelTurnPolicy.Retry.MaximumAttempts = 1
			environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
			if err := environment.GetWorkflowError(); err != nil {
				t.Fatalf("AgentWorkflow error = %v", err)
			}
			var result workflows.AgentWorkflowResult
			if err := environment.GetWorkflowResult(&result); err != nil || result.Failure == nil || !result.Failure.Is(test.want) {
				t.Fatalf("GetWorkflowResult() error = %v, result = %#v", err, result)
			}
		})
	}
}

func TestAgentWorkflowRequestsCancellationOfTheActiveTool(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	var lifecycle []agentactivities.LifecycleInput
	registerAgentLifecycle(environment, &lifecycle)
	environment.SetWorkerOptions(worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: agent.ConversationRef{Revision: 0, Bytes: 50}}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeToolCalls, ToolsetFingerprint: testToolsetFingerprint,
			ConversationRef: agent.ConversationRef{Revision: 1, Bytes: 100},
			ToolCalls:       []agent.PendingToolCall{{CallID: "call_slow", Name: "exec_command"}}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	toolCancelled := false
	environment.SetOnActivityCanceledListener(func(info *activity.Info) {
		if info.ActivityType.Name == agent.ToolActivityName {
			toolCancelled = true
		}
	})
	environment.RegisterActivityWithOptions(func(ctx context.Context, _ agent.ToolInput) (agent.ToolOutput, error) {
		environment.CancelWorkflow()
		<-ctx.Done()
		return agent.ToolOutput{}, ctx.Err()
	}, activity.RegisterOptions{Name: agent.ToolActivityName})
	environment.ExecuteWorkflow(workflows.AgentWorkflow, validAgentWorkflowInput(work.StageImplement))
	if !toolCancelled {
		t.Fatal("active tool activity did not observe cancellation")
	}
	if !temporal.IsCanceledError(environment.GetWorkflowError()) {
		t.Fatalf("workflow error = %v, want cancellation", environment.GetWorkflowError())
	}
	if len(lifecycle) != 1 || lifecycle[0].Outcome != telemetry.AgentOutcomeCancelled {
		t.Fatalf("lifecycle = %#v, want one cancellation", lifecycle)
	}
}

func TestAgentWorkflowContinuesAsNewWithOnlyReferences(t *testing.T) {
	t.Parallel()

	const conversationBody = "large-conversation-content-must-not-enter-the-continuation-payload"
	initial := agent.ConversationRef{Key: "conversations/run-7/0", Revision: 0, Bytes: 100, Digest: "initial"}
	continued := agent.ConversationRef{Key: "conversations/run-7/16", Revision: 16, Bytes: 300, Digest: "continued"}
	transcript := agent.TranscriptRef{Key: "transcripts/run-7", Bytes: 80, Digest: "transcript"}
	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	var lifecycle []agentactivities.LifecycleInput
	registerAgentLifecycle(environment, &lifecycle)
	environment.SetWorkerOptions(worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: initial, TranscriptRef: transcript}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	environment.RegisterActivityWithOptions(func(_ context.Context, input agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeToolCalls, ToolsetFingerprint: testToolsetFingerprint,
			ConversationRef: agent.ConversationRef{Key: fmt.Sprintf("conversations/run-7/%d", input.ModelTurn*2-1), Revision: input.ModelTurn*2 - 1, Bytes: 200, Digest: "requested"},
			ToolCalls:       []agent.PendingToolCall{{CallID: fmt.Sprintf("call_%d", input.ModelTurn), Name: "read_file"}},
			Usage:           work.Usage{InputTokens: 10, OutputTokens: 2}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(_ context.Context, input agent.ToolInput) (agent.ToolOutput, error) {
		turn := input.ConversationRef.Revision/2 + 1
		ref := agent.ConversationRef{Key: fmt.Sprintf("conversations/run-7/%d", turn*2), Revision: turn * 2, Bytes: 300, Digest: "continued"}
		return agent.ToolOutput{CallID: input.Call.CallID, ConversationRef: ref}, nil
	}, activity.RegisterOptions{Name: agent.ToolActivityName})
	input := validAgentWorkflowInput(work.StageImplement)
	identity := work.RunWorkerIdentity{RunID: "019fb900-0000-7000-8000-000000000001", Generation: 2}
	input.Attempt.Key.RunID = identity.RunID
	input.Attempt.Key.Turn = 2
	input.Identity = "agent/019fb900-0000-7000-8000-000000000001/step/9/attempt/2"
	input.ToolTarget = agent.ToolTarget{Kind: agent.ToolTargetRunWorker, RunWorkerIdentity: identity}
	input.Attempt.Detail = work.TicketDetail{Ticket: work.Ticket{Number: 7, Body: conversationBody}}
	input.Seed = &agent.ConversationSeed{
		Source:          work.StageKey{Ticket: 7, RunID: identity.RunID, Stage: work.StageImplement, Turn: 1},
		SourceIdentity:  "agent/019fb900-0000-7000-8000-000000000001/step/8/attempt/1",
		ConversationRef: agent.ConversationRef{Key: "conversations/agent/seed/0/digest", Bytes: 1, Digest: "digest"},
	}
	environment.SetContinueAsNewSuggested(true)
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)

	var continuedAsNew *workflow.ContinueAsNewError
	if !errors.As(environment.GetWorkflowError(), &continuedAsNew) {
		t.Fatalf("workflow error = %v, want ContinueAsNew", environment.GetWorkflowError())
	}
	if len(lifecycle) != 0 {
		t.Fatalf("ContinueAsNew recorded terminal lifecycle: %#v", lifecycle)
	}
	var next workflows.AgentWorkflowInput
	if err := converter.GetDefaultDataConverter().FromPayloads(continuedAsNew.Input, &next); err != nil {
		t.Fatalf("decode continued input: %v", err)
	}
	if next.State == nil || next.State.ConversationRef != initial || next.State.TranscriptRef != transcript ||
		next.State.ToolsetFingerprint != "" || next.State.ModelTurns != 0 ||
		next.State.ToolCalls != 0 || next.State.Usage.InputTokens != 0 {
		t.Fatalf("continued state = %#v", next.State)
	}
	if next.ToolTarget != input.ToolTarget {
		t.Fatalf("continued tool target = %#v, want %#v", next.ToolTarget, input.ToolTarget)
	}
	if next.ModelTurnPolicy != input.ModelTurnPolicy {
		t.Fatalf("continued model-turn policy = %#v, want %#v", next.ModelTurnPolicy, input.ModelTurnPolicy)
	}
	if next.ControlPolicy != input.ControlPolicy {
		t.Fatalf("continued control policy = %#v, want %#v", next.ControlPolicy, input.ControlPolicy)
	}
	if next.Seed == nil || *next.Seed != *input.Seed {
		t.Fatalf("continued seed = %#v, want %#v", next.Seed, input.Seed)
	}
	continuedInput, err := json.Marshal(next)
	if err != nil {
		t.Fatalf("marshal continued input: %v", err)
	}
	if strings.Contains(string(continuedInput), `"limits"`) || strings.Contains(string(continuedInput), `"MaxReviewSteps"`) {
		t.Fatalf("fresh continued input retained removed budget policy: %s", continuedInput)
	}
	if next.Attempt.Detail != (work.TicketDetail{}) || next.Attempt.Prior.Plan.Prose() != "" ||
		next.Attempt.Prior.LatestImplement.Prose() != "" || next.Attempt.Prior.LatestReview.Prose() != "" ||
		len(next.Attempt.Prior.ReviewLedger) != 0 {
		t.Fatalf("continued attempt retained prompt content: %#v", next.Attempt)
	}
	for _, payload := range continuedAsNew.Input.Payloads {
		if strings.Contains(string(payload.Data), conversationBody) {
			t.Fatal("continued payload contains the initial conversation body")
		}
	}
}

func TestAgentWorkflowResumesFromReferencesWithoutPreparingAgain(t *testing.T) {
	t.Parallel()

	conversation := agent.ConversationRef{Key: "conversations/run-7/2", Revision: 2, Bytes: 300, Digest: "continued"}
	textRef := agent.TextRef{Key: "conversations/run-7/final", Bytes: 20, Digest: "final"}
	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	environment.RegisterActivityWithOptions(func(_ context.Context, input agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		if input.ModelTurn != 2 || input.ConversationRef != conversation {
			t.Fatalf("resumed model input = %#v", input)
		}
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeFinalText, ToolsetFingerprint: testToolsetFingerprint,
			ConversationRef: conversation, FinalTextRef: textRef,
			Usage: work.Usage{InputTokens: 4, OutputTokens: 2}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.FinalizeInput) (agentactivities.FinalizeOutput, error) {
		return testAgentFinalizeOutput(work.NewStageOutput(work.StageImplement, work.ImplementOutput{Report: "resumed"})), nil
	}, activity.RegisterOptions{Name: agent.FinalizeActivityName})
	input := validAgentWorkflowInput(work.StageImplement)
	input.State = &workflows.AgentWorkflowState{
		ConversationRef: conversation, ToolsetFingerprint: testToolsetFingerprint,
		Usage: work.Usage{InputTokens: 10, OutputTokens: 3}, UsageMeasured: true, ModelTurns: 1,
	}
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult() error = %v", err)
	}
	if result.Result.Prose() != "resumed" || result.ModelTurns != 2 || result.Usage.InputTokens != 14 || result.Usage.OutputTokens != 5 {
		t.Fatalf("resumed result = %#v", result)
	}
}

func TestAgentWorkflowDoesNotStopAtResourceCounts(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	environment.SetWorkerOptions(worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: agent.ConversationRef{Revision: 0, Bytes: 50}}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	turns := 0
	environment.RegisterActivityWithOptions(func(context.Context, agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		turns++
		if turns == 1 {
			return agent.ModelTurnResult{
				Outcome: agent.OutcomeToolCalls, ToolsetFingerprint: testToolsetFingerprint,
				ConversationRef: agent.ConversationRef{Revision: 1, Bytes: 2 << 20},
				ToolCalls:       []agent.PendingToolCall{{CallID: "call_1", Name: "read_file"}},
				Usage:           work.Usage{InputTokens: 600_000, OutputTokens: 120_000}, UsageMeasured: true,
			}, nil
		}
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeFinalText, ToolsetFingerprint: testToolsetFingerprint,
			ConversationRef: agent.ConversationRef{Revision: 3, Bytes: 3 << 20}, FinalTextRef: agent.TextRef{Key: "text"},
			Usage: work.Usage{InputTokens: 600_000, OutputTokens: 120_000}, UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(_ context.Context, input agent.ToolInput) (agent.ToolOutput, error) {
		return agent.ToolOutput{CallID: input.Call.CallID, ConversationRef: agent.ConversationRef{Revision: 2, Bytes: 2 << 20}}, nil
	}, activity.RegisterOptions{Name: agent.ToolActivityName})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.FinalizeInput) (agentactivities.FinalizeOutput, error) {
		return testAgentFinalizeOutput(work.NewStageOutput(work.StageImplement, work.ImplementOutput{Report: "done"})), nil
	}, activity.RegisterOptions{Name: agent.FinalizeActivityName})
	input := validAgentWorkflowInput(work.StageImplement)
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil || result.Failure != nil || result.Result.Prose() != "done" {
		t.Fatalf("GetWorkflowResult() error = %v, result = %#v", err, result)
	}
}

func TestAgentWorkflowExecutesARequestedToolAndContinuesWithItsOutput(t *testing.T) {
	t.Parallel()

	initial := agent.ConversationRef{Key: "conversations/agent/run-7/implement/1/0/initial", Revision: 0, Bytes: 100, Digest: "initial"}
	requested := agent.ConversationRef{Key: "conversations/agent/run-7/implement/1/1/requested", Revision: 1, Bytes: 100, Digest: "requested"}
	continued := agent.ConversationRef{Key: "conversations/agent/run-7/implement/1/2/continued", Revision: 2, Bytes: 100, Digest: "continued"}
	argumentsRef := agent.ArgumentsRef{Key: "conversations/args", Bytes: 10, Digest: "args"}
	textRef := agent.TextRef{Key: "conversations/text", Bytes: 10, Digest: "text"}
	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	environment.SetWorkerOptions(worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	environment.RegisterActivityWithOptions(
		func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
			return agentactivities.PrepareOutput{ConversationRef: initial}, nil
		}, activity.RegisterOptions{Name: agent.PrepareActivityName},
	)
	modelTurns := 0
	environment.RegisterActivityWithOptions(
		func(_ context.Context, input agent.ModelTurnInput) (agent.ModelTurnResult, error) {
			modelTurns++
			if modelTurns == 1 {
				return agent.ModelTurnResult{
					Outcome: agent.OutcomeToolCalls, ToolsetFingerprint: testToolsetFingerprint, ConversationRef: requested,
					ToolCalls:     []agent.PendingToolCall{{CallID: "call_1", Name: "read_file", ArgumentsRef: argumentsRef}},
					UsageMeasured: true,
				}, nil
			}
			if input.ConversationRef != continued {
				t.Fatalf("continuation conversation = %#v", input.ConversationRef)
			}
			return agent.ModelTurnResult{Outcome: agent.OutcomeFinalText, ToolsetFingerprint: testToolsetFingerprint, ConversationRef: continued, FinalTextRef: textRef, UsageMeasured: true}, nil
		}, activity.RegisterOptions{Name: agent.ModelTurnActivityName},
	)
	toolCalls := 0
	environment.RegisterActivityWithOptions(
		func(_ context.Context, input agent.ToolInput) (agent.ToolOutput, error) {
			toolCalls++
			if input.ConversationRef != requested || input.Call.CallID != "call_1" {
				t.Fatalf("tool input = %#v", input)
			}
			return agent.ToolOutput{CallID: "call_1", ConversationRef: continued}, nil
		}, activity.RegisterOptions{Name: agent.ToolActivityName},
	)
	environment.RegisterActivityWithOptions(
		func(context.Context, agentactivities.FinalizeInput) (agentactivities.FinalizeOutput, error) {
			return testAgentFinalizeOutput(work.NewStageOutput(work.StageImplement, work.ImplementOutput{Report: "done"})), nil
		}, activity.RegisterOptions{Name: agent.FinalizeActivityName},
	)
	input := validAgentWorkflowInput(work.StageImplement)
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult() error = %v", err)
	}
	if modelTurns != 2 || toolCalls != 1 || result.ModelTurns != 2 || result.ToolCalls != 1 || result.ConversationRef != continued || result.Result.Prose() != "done" {
		t.Fatalf("turns model=%d tool=%d result=%#v", modelTurns, toolCalls, result)
	}
}

func TestAgentWorkflowRejectsToolsetFingerprintChangeBetweenTurns(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	environment.SetWorkerOptions(worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	environment.RegisterActivityWithOptions(func(context.Context, agentactivities.PrepareInput) (agentactivities.PrepareOutput, error) {
		return agentactivities.PrepareOutput{ConversationRef: agent.ConversationRef{Revision: 0, Bytes: 10}}, nil
	}, activity.RegisterOptions{Name: agent.PrepareActivityName})
	turns := 0
	environment.RegisterActivityWithOptions(func(_ context.Context, input agent.ModelTurnInput) (agent.ModelTurnResult, error) {
		turns++
		if turns == 1 {
			if input.ToolsetFingerprint != "" {
				t.Fatalf("first turn fingerprint = %q, want unresolved", input.ToolsetFingerprint)
			}
			return agent.ModelTurnResult{
				Outcome: agent.OutcomeToolCalls, ToolsetFingerprint: testToolsetFingerprint,
				ConversationRef: agent.ConversationRef{Revision: 1, Bytes: 20},
				ToolCalls:       []agent.PendingToolCall{{CallID: "call_1", Name: "read_file"}}, UsageMeasured: true,
			}, nil
		}
		if input.ToolsetFingerprint != testToolsetFingerprint {
			t.Fatalf("second turn fingerprint = %q, want pinned", input.ToolsetFingerprint)
		}
		return agent.ModelTurnResult{
			Outcome: agent.OutcomeFinalText, ToolsetFingerprint: "sha256:changed",
			ConversationRef: agent.ConversationRef{Revision: 3, Bytes: 40}, FinalTextRef: agent.TextRef{Key: "text"},
			UsageMeasured: true,
		}, nil
	}, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	environment.RegisterActivityWithOptions(func(_ context.Context, input agent.ToolInput) (agent.ToolOutput, error) {
		if input.ToolsetFingerprint != testToolsetFingerprint {
			t.Fatalf("tool fingerprint = %q, want pinned", input.ToolsetFingerprint)
		}
		return agent.ToolOutput{CallID: input.Call.CallID, ConversationRef: agent.ConversationRef{Revision: 2, Bytes: 30}}, nil
	}, activity.RegisterOptions{Name: agent.ToolActivityName})

	environment.ExecuteWorkflow(workflows.AgentWorkflow, validAgentWorkflowInput(work.StageImplement))
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("AgentWorkflow error = %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := environment.GetWorkflowResult(&result); err != nil || result.Failure == nil || !result.Failure.Is(agent.TerminalFailureInvalidProviderOutcome) {
		t.Fatalf("GetWorkflowResult() error = %v, result = %#v", err, result)
	}
}

func TestAgentWorkflowRejectsInvalidRunWorkerToolTarget(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	input := validAgentWorkflowInput(work.StageImplement)
	input.ToolTarget = agent.ToolTarget{
		Kind:              agent.ToolTargetRunWorker,
		RunWorkerIdentity: work.RunWorkerIdentity{RunID: "019fb900-0000-7000-8000-000000000002", Generation: 1},
	}
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	var applicationError *temporal.ApplicationError
	if !errors.As(environment.GetWorkflowError(), &applicationError) || applicationError.Type() != agent.ErrorTypeInvalidInput || !applicationError.NonRetryable() {
		t.Fatalf("workflow error = %v, want non-retryable invalid target", environment.GetWorkflowError())
	}
}

func TestAgentWorkflowRejectsAnInvalidModelTurnPolicy(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	input := validAgentWorkflowInput(work.StageImplement)
	input.ModelTurnPolicy.HeartbeatTimeout = input.ModelTurnPolicy.StartToCloseTimeout
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	var applicationError *temporal.ApplicationError
	if !errors.As(environment.GetWorkflowError(), &applicationError) || applicationError.Type() != agent.ErrorTypeInvalidInput || !applicationError.NonRetryable() {
		t.Fatalf("workflow error = %v, want non-retryable invalid model-turn policy", environment.GetWorkflowError())
	}
}

func TestAgentWorkflowRejectsAnInvalidControlPolicy(t *testing.T) {
	t.Parallel()

	suite := &testsuite.WorkflowTestSuite{}
	environment := suite.NewTestWorkflowEnvironment()
	registerAgentLifecycle(environment, nil)
	input := validAgentWorkflowInput(work.StageImplement)
	input.ControlPolicy.ScheduleToCloseTimeout = input.ControlPolicy.StartToCloseTimeout / 2
	environment.ExecuteWorkflow(workflows.AgentWorkflow, input)
	var applicationError *temporal.ApplicationError
	if !errors.As(environment.GetWorkflowError(), &applicationError) || applicationError.Type() != agent.ErrorTypeInvalidInput || !applicationError.NonRetryable() {
		t.Fatalf("workflow error = %v, want non-retryable invalid control policy", environment.GetWorkflowError())
	}
}

func TestAgentToolActivityOutlivesTheLongestToolCommand(t *testing.T) {
	got := workflows.AgentToolActivityOptionsForTest().StartToCloseTimeout
	if got <= agent.MaxToolExecutionDuration {
		t.Fatalf("tool activity timeout = %s, must exceed command timeout %s so completion can be persisted", got, agent.MaxToolExecutionDuration)
	}
	if got != 31*time.Minute {
		t.Fatalf("tool activity timeout = %s, want the 30 minute command bound plus persistence margin", got)
	}
}

func testAgentFinalizeOutput(result work.StageOutput) agentactivities.FinalizeOutput {
	return agentactivities.FinalizeOutput{Result: &result}
}

func validAgentWorkflowInput(stage work.Stage) workflows.AgentWorkflowInput {
	return workflows.AgentWorkflowInput{
		Attempt: activities.StageAttempt{
			Key:   work.StageKey{Ticket: 7, RunID: testAgentRunID, Stage: stage, Turn: 1},
			Model: work.Model{Name: "gpt-test", Effort: "medium"},
		},
		ToolTarget: agent.ToolTarget{
			Kind:              agent.ToolTargetRunWorker,
			RunWorkerIdentity: work.RunWorkerIdentity{RunID: testAgentRunID, Generation: 1},
		},
		ToolsetID: "coding-write-v1", CacheKey: "run-7-stage",
		ModelTurnPolicy: work.DefaultTargetRunPolicy().Agent,
		ControlPolicy:   work.DefaultTargetRunPolicy().Recording,
	}
}

func registerAgentLifecycle(environment *testsuite.TestWorkflowEnvironment, recorded *[]agentactivities.LifecycleInput) {
	environment.RegisterActivityWithOptions(func(_ context.Context, input agentactivities.LifecycleInput) error {
		if recorded != nil {
			*recorded = append(*recorded, input)
		}
		return nil
	}, activity.RegisterOptions{Name: agent.LifecycleActivityName})
}
