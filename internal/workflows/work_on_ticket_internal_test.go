package workflows

import (
	"time"

	"errors"
	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/work"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

var _ = Describe("workflow internals", func() {
	It("uses one absolute deadline for remaining session execution", func() {
		deadline := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

		first, err := remainingSessionExecutionTimeout(deadline.Add(-20*time.Hour), deadline)
		Expect(err).NotTo(HaveOccurred(), "initial remaining timeout")
		Expect(first).To(Equal(20*time.Hour), "first remaining session timeout")
		replacement, err := remainingSessionExecutionTimeout(deadline.Add(-time.Hour), deadline)
		Expect(err).NotTo(HaveOccurred(), "replacement remaining timeout")
		Expect(replacement).To(Equal(time.Hour), "replacement timeout")

		_, err = remainingSessionExecutionTimeout(deadline, deadline)
		var application *temporal.ApplicationError
		Expect(errors.As(err, &application)).To(BeTrue(), "elapsed deadline error should be typed")
		Expect(application.Type()).To(Equal(activities.ErrTypeHardDeadline), "error type")
	})

	Describe("continueAsNew policy carry-over", func() {
		DescribeTable("continueAgentWorkflowAsNew preserves only what history needs",
			func(tc struct {
				name      string
				unbounded bool
			}) {
				legacy := defaultLegacyAgentLimits()
				input := AgentWorkflowInput{LegacyLimits: legacy}
				state := AgentWorkflowState{ConversationRef: agent.ConversationRef{Key: "conversation"}}
				suite := &testsuite.WorkflowTestSuite{}
				environment := suite.NewTestWorkflowEnvironment()
				environment.ExecuteWorkflow(func(ctx workflow.Context) error {
					return continueAgentWorkflowAsNew(ctx, input, state, tc.unbounded)
				})
				var continued *workflow.ContinueAsNewError
				Expect(errors.As(environment.GetWorkflowError(), &continued)).To(BeTrue(), "workflow error")
				var next AgentWorkflowInput
				Expect(converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next)).NotTo(HaveOccurred(), "decode continued input")
				if tc.unbounded {
					Expect(next.LegacyLimits).To(BeNil(), "unbounded continuation should drop legacy limits")
					return
				}
				Expect(next.LegacyLimits).NotTo(BeNil(), "legacy continuation should retain legacy limits")
				Expect(*next.LegacyLimits).To(Equal(*legacy), "legacy continuation limits")
			},
			Entry("legacy replay retains legacy wire fields", struct {
				name      string
				unbounded bool
			}{name: "legacy replay retains legacy wire fields", unbounded: false}),
			Entry("unbounded execution drops legacy wire fields", struct {
				name      string
				unbounded bool
			}{name: "unbounded execution drops legacy wire fields", unbounded: true}),
		)
	})

	DescribeTable("target failures needing fresh attempts",
		func(kind agent.TerminalFailureKind, want bool) {
			failure := &agent.TerminalFailure{Kind: kind}
			Expect(targetFailureNeedsFreshAttempt(failure)).To(Equal(want), "targetFailureNeedsFreshAttempt(%s)", kind)
		},
		Entry("session lost", agent.TerminalFailureSessionLost, false),
		Entry("ambiguous tool execution", agent.TerminalFailureAmbiguousToolExecution, true),
		Entry("invalid provider outcome", agent.TerminalFailureInvalidProviderOutcome, true),
		Entry("model exhausted", agent.TerminalFailureModelExhausted, false),
		Entry("rate limited", agent.TerminalFailureRateLimited, false),
		Entry("authentication", agent.TerminalFailureAuthentication, false),
	)

	It("builds agent child options that allow same-attempt recovery", func() {
		policy := work.DefaultTargetRunPolicy().Agent
		options := targetAgentChildOptions("agent/run-1/step/5/attempt/1", policy)
		Expect(options.WorkflowID).To(Equal("agent/run-1/step/5/attempt/1"))
		Expect(options.WorkflowExecutionTimeout).To(Equal(policy.ScheduleToCloseTimeout))
		Expect(options.WorkflowIDReusePolicy).To(Equal(enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE))
		Expect(options.WaitForCancellation).To(BeTrue())
		Expect(options.ParentClosePolicy).To(Equal(enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL))
	})

	It("exposes the workflow hard deadline as execution timeout", func() {
		policy := work.DefaultTargetRunPolicy()
		policy.HardDeadline = 30 * time.Hour
		Expect(WorkOnTicketExecutionTimeout(policy)).To(Equal(30 * time.Hour))
	})
})
