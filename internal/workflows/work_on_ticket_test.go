package workflows_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TestWorkOnTicketClaimsBeforeProvisioningGenerationOneAndClonesThroughItsSession
// holds the first target-run boundary: the Store records ownership before a
// Run Worker exists, Session creation is the readiness handoff, and clone is
// the first repository-affine activity on that worker's queue.
func TestWorkOnTicketClaimsBeforeProvisioningGenerationOneAndClonesThroughItsSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recorderStore := storefake.New()
	ticket, err := recorderStore.CreateTicket(ctx, "target run", "clone the repository", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	input := workflows.WorkOnTicketInput{
		TicketID: ticket.ID,
		RunID:    "0f466627-b3ae-4ba2-9c96-6ef44ec6f578",
		Policy:   work.DefaultTargetRunPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	}

	winner := newWorkOnTicketHarness(t, recorderStore)
	winner.run(input)
	if err := winner.env.GetWorkflowError(); err != nil {
		t.Fatalf("winning WorkOnTicket: %v", err)
	}
	if winner.provisioned.Identity.Generation != 1 {
		t.Fatalf("provisioned generation = %d, want 1", winner.provisioned.Identity.Generation)
	}
	claimed, err := recorderStore.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if claimed.State != store.TicketDone || claimed.ActiveRunID != "" {
		t.Fatalf("completed ticket = %+v, want done with no active owner", claimed)
	}
	if winner.clone.Step.StepOrdinal != 3 || winner.clone.Step.Branch != winner.provisioned.Branch || winner.clone.CloneURL != input.CloneURL {
		t.Fatalf("clone = %+v, provision = %+v", winner.clone, winner.provisioned)
	}
	loser := newWorkOnTicketHarness(t, recorderStore)
	loserInput := input
	loserInput.RunID = "0f466627-b3ae-4ba2-9c96-6ef44ec6f579"
	loser.run(loserInput)
	if err := loser.env.GetWorkflowError(); err == nil {
		t.Fatal("losing WorkOnTicket succeeded")
	}
	if loser.provisioned.Identity != (work.RunWorkerIdentity{}) || loser.clone.Step != (activities.RepositoryStep{}) {
		t.Fatalf("losing WorkOnTicket reached private work: provision = %+v, clone = %+v", loser.provisioned, loser.clone)
	}
}

// TestWorkOnTicketConfirmsMergeBeforeBestEffortTeardown specifies one complete
// target happy path. The Store is the public durable seam: every primary
// operation owns one ordered Step, only agent Steps own Attempts, and a
// confirmed squash merge makes the still-owned Ticket done before teardown.
func TestWorkOnTicketConfirmsMergeBeforeBestEffortTeardown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recorderStore := storefake.New()
	ticket, err := recorderStore.CreateTicket(ctx, "target run", "finish the ticket", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	input := workflows.WorkOnTicketInput{
		TicketID: ticket.ID,
		RunID:    "f37fcbca-b509-4823-8e7d-f7c7462b9dc8",
		Policy:   work.DefaultTargetRunPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	}

	h := newWorkOnTicketHarness(t, recorderStore)
	h.run(input)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}

	detail, err := recorderStore.TargetRunDetail(ctx, input.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	wantSteps := []work.StepKind{
		work.StepCreateRunWorker,
		work.StepAcquireRunWorkerSession,
		work.StepCloneRepository,
		work.StepPlan,
		work.StepImplement,
		work.StepSyncPullRequest,
		work.StepAwaitCI,
		work.StepReview,
		work.StepMarkPullRequestReady,
		work.StepMergePullRequest,
	}
	if len(detail.Steps) != len(wantSteps) {
		t.Fatalf("step count = %d, want %d (%v)", len(detail.Steps), len(wantSteps), detail.Steps)
	}
	for index, want := range wantSteps {
		step := detail.Steps[index]
		if step.Step.Kind != want || step.Step.Ordinal != index+1 || step.Step.State != work.StepStateCompleted {
			t.Fatalf("step %d = %+v, want completed %s", index+1, step.Step, want)
		}
		wantAttempts := 0
		if want == work.StepPlan || want == work.StepImplement || want == work.StepReview {
			wantAttempts = 1
		}
		if len(step.Attempts) != wantAttempts {
			t.Fatalf("step %s attempts = %d, want %d", want, len(step.Attempts), wantAttempts)
		}
		if index < 2 {
			var outcome struct {
				Kind       string `json:"kind"`
				Generation int    `json:"generation"`
			}
			if err := json.Unmarshal(step.Step.Result, &outcome); err != nil {
				t.Fatalf("decode %s outcome: %v", want, err)
			}
			wantKind := "created"
			if want == work.StepAcquireRunWorkerSession {
				wantKind = "acquired"
			}
			if outcome.Kind != wantKind || outcome.Generation != 1 {
				t.Fatalf("%s outcome = %+v, want %s generation 1", want, outcome, wantKind)
			}
		}
	}

	if h.ci.Step.PushedHead != "H1" || h.ci.CI.CommitSHA != "H1" {
		t.Fatalf("CI was not bound to H1: %+v", h.ci)
	}
	if h.reviewHead != "H1" {
		t.Fatalf("review candidate head = %q, want H1", h.reviewHead)
	}
	if h.merge.ExpectedHeadSHA != "H1" || h.merge.Step.PushedHead != "H1" {
		t.Fatalf("merge was not bound to H1: %+v", h.merge)
	}
	if h.ready.Step.PullRequestNodeID != "PR_node1" {
		t.Fatalf("ready input = %+v, want authoritative PR node", h.ready)
	}
	if len(h.rotations) != 3 {
		t.Fatalf("credential rotations = %d, want one per agent attempt", len(h.rotations))
	}
	for index, child := range h.agentInputs {
		if child.ToolTarget.RunWorkerIdentity != h.provisioned.Identity || child.Identity == "" || child.Identity != h.agentChildIDs[index] {
			t.Fatalf("agent child %s identity = %q / target %+v", child.Attempt.Key.Stage, child.Identity, child.ToolTarget)
		}
	}
	if len(h.finalizedAttempts) != 3 {
		t.Fatalf("finalized agent evidence = %+v, want plan, implement, and review before their Steps completed", h.finalizedAttempts)
	}

	storedTicket, err := recorderStore.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if storedTicket.State != store.TicketDone || storedTicket.ActiveRunID != "" {
		t.Fatalf("terminal ticket = %+v, want done with no owner", storedTicket)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeSucceeded || detail.Run.ReviewedHead != "H1" || detail.Run.MergeSHA != "M1" {
		t.Fatalf("terminal run = %+v, want confirmed H1/M1 success", detail.Run)
	}
	if len(h.deleted) != int(input.Policy.Teardown.Retry.MaximumAttempts) || h.deleted[0].Identity != h.provisioned.Identity {
		t.Fatalf("teardown calls = %+v, want %d bounded retries for the successful worker", h.deleted, input.Policy.Teardown.Retry.MaximumAttempts)
	}
}

func TestWorkOnTicketDerivesTheRunIdentityFromItsTemporalExecution(t *testing.T) {
	t.Parallel()
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	var claimed store.ClaimRunInput
	env.RegisterActivityWithOptions(
		func(_ context.Context, input store.ClaimRunInput) (store.ClaimRunResult, error) {
			claimed = input
			return store.ClaimRunResult{}, temporal.NewNonRetryableApplicationError("captured authoritative run ID", activities.ErrTypeInvalid, nil)
		},
		activity.RegisterOptions{Name: "ClaimAndStartRun"},
	)
	env.ExecuteWorkflow(workflows.WorkOnTicket, workflows.WorkOnTicketInput{
		TicketID: 1,
		Policy:   work.DefaultTargetRunPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	})

	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("WorkOnTicket succeeded after the capture activity failed")
	}
	if got, want := claimed.RunID, "default-test-run-id"; got != want {
		t.Fatalf("claimed Run ID = %q, want authoritative Temporal Run ID %q", got, want)
	}
}

// A red CI result is completed feedback, not a workflow failure. The next
// implement Step must be a new durable Step but continue the surviving
// generation's implementer thread, then send the new authoritative head
// through CI and a fresh review before merge.
func TestWorkOnTicketRepairsRedCIThenReviewsTheNewHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recorderStore := storefake.New()
	ticket, err := recorderStore.CreateTicket(ctx, "repair red CI", "make CI green", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	input := workflows.WorkOnTicketInput{
		TicketID: ticket.ID,
		RunID:    "019fb901-0000-7000-8000-000000000001",
		Policy:   work.DefaultTargetRunPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	}

	h := newWorkOnTicketHarness(t, recorderStore)
	h.sync = func(in activities.TargetSyncPullRequestInput) (work.PullRequest, error) {
		head := "H1"
		if len(h.syncInputs) == 2 {
			head = "H2"
		}
		position := in.Step
		position.PushedHead = head
		position.PullRequestNumber, position.PullRequestNodeID = 1, "PR_node1"
		if err := h.checkpointRepositoryStep(position); err != nil {
			return work.PullRequest{}, err
		}
		return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: head, Draft: true}, nil
	}
	h.awaitCI = func(in activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		if err := h.checkpointRepositoryStep(in.Step); err != nil {
			return activities.AwaitCIOutput{}, err
		}
		if in.CI.CommitSHA == "H1" {
			return activities.AwaitCIOutput{CommitSHA: "H1", Green: false, RedFailures: []work.CheckFailure{{Name: "test-software-factory", Fingerprint: "ci-red", Evidence: "expected true to be false"}}}, nil
		}
		return activities.AwaitCIOutput{CommitSHA: "H2", Green: true}, nil
	}
	h.run(input)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}

	detail, err := recorderStore.TargetRunDetail(ctx, input.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	wantSteps := []work.StepKind{
		work.StepCreateRunWorker, work.StepAcquireRunWorkerSession,
		work.StepCloneRepository, work.StepPlan, work.StepImplement,
		work.StepSyncPullRequest, work.StepAwaitCI, work.StepImplement,
		work.StepSyncPullRequest, work.StepAwaitCI, work.StepReview,
		work.StepMarkPullRequestReady, work.StepMergePullRequest,
	}
	if len(detail.Steps) != len(wantSteps) {
		t.Fatalf("steps = %d, want %d: %+v", len(detail.Steps), len(wantSteps), detail.Steps)
	}
	for index, want := range wantSteps {
		if got := detail.Steps[index].Step.Kind; got != want {
			t.Fatalf("step %d kind = %q, want %q", index+1, got, want)
		}
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	reviews := agentChildrenAtStage(h.agentInputs, work.StageReview)
	if len(implements) != 2 || implements[0].Seed != nil || implements[1].Seed == nil || implements[1].Seed.SourceIdentity != implements[0].Identity || implements[1].Seed.ConversationRef.Key != implements[0].Identity+"/conversation" {
		t.Fatalf("implement feedback seed = %+v, want the first completed implement conversation", implements)
	}
	if len(reviews) != 1 || reviews[0].Attempt.PromptContext.CandidateHeadSHA != "H2" || reviews[0].Seed != nil {
		t.Fatalf("reviews = %+v, want one fresh H2 review", reviews)
	}
	if h.merge.ExpectedHeadSHA != "H2" {
		t.Fatalf("merge = %+v, want only reviewed H2", h.merge)
	}
}

// A blocking review is completed, authoritative feedback. It must reopen the
// surviving implementer with both the reviewed head and structured findings,
// then bind CI and an independent reviewer to the new head before merge.
func TestWorkOnTicketRepairsBlockingReviewWithFreshCandidateAuthorization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "review feedback", "repair the finding", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000007", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.sync = func(input activities.TargetSyncPullRequestInput) (work.PullRequest, error) {
		head := "H1"
		if len(h.syncInputs) == 2 {
			head = "H2"
		}
		position := input.Step
		position.PushedHead, position.PullRequestNumber, position.PullRequestNodeID = head, 1, "PR_node1"
		if err := h.checkpointRepositoryStep(position); err != nil {
			return work.PullRequest{}, err
		}
		return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: head, Draft: true}, nil
	}
	h.awaitCI = func(input activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		if err := h.checkpointRepositoryStep(input.Step); err != nil {
			return activities.AwaitCIOutput{}, err
		}
		return activities.AwaitCIOutput{CommitSHA: input.CI.CommitSHA, Green: true}, nil
	}
	reviews := 0
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		result := targetAgentWorkflowResult(t, input)
		if input.Attempt.Key.Stage != work.StageReview {
			return result, nil
		}
		reviews++
		if reviews != 1 {
			return result, nil
		}
		var output work.StageOutput
		raw := `{"stage":"review","value":{"document":"blocked","findings":[{"id":"finding_1","blocking":true,"summary":"repair the boundary"}]}}`
		if err := json.Unmarshal([]byte(raw), &output); err != nil {
			return workflows.AgentWorkflowResult{}, err
		}
		result.Result = output
		return result, nil
	}
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}

	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	reviewInputs := agentChildrenAtStage(h.agentInputs, work.StageReview)
	if len(implements) != 2 || implements[1].Seed == nil || implements[1].Seed.SourceIdentity != implements[0].Identity || implements[1].Attempt.PromptContext.CandidateHeadSHA != "H1" || len(implements[1].Attempt.PromptContext.ReviewFindings) != 1 || implements[1].Attempt.PromptContext.ReviewFindings[0].ID != "finding_1" {
		t.Fatalf("review-feedback implementation = %+v, want seeded H1 implementer handoff with typed finding", implements)
	}
	if len(h.ciInputs) != 2 || h.ciInputs[0].CI.CommitSHA != "H1" || h.ciInputs[1].CI.CommitSHA != "H2" || len(reviewInputs) != 2 || reviewInputs[0].Attempt.PromptContext.CandidateHeadSHA != "H1" || reviewInputs[1].Attempt.PromptContext.CandidateHeadSHA != "H2" || reviewInputs[0].Seed != nil || reviewInputs[1].Seed != nil {
		t.Fatalf("fresh candidate authorization = CI %+v, reviews %+v", h.ciInputs, reviewInputs)
	}
	if h.merge.ExpectedHeadSHA != "H2" {
		t.Fatalf("merge = %+v, want only reviewed H2", h.merge)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	attempts := 0
	for _, step := range detail.Steps {
		attempts += len(step.Attempts)
	}
	if attempts != 5 {
		t.Fatalf("cumulative attempts = %d, want five without a loop reset", attempts)
	}
}

func TestWorkOnTicketNeverMergesAHeadChangedAfterReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recorderStore := storefake.New()
	ticket, err := recorderStore.CreateTicket(ctx, "head changed", "review the new head", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	input := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000002", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, recorderStore)
	h.awaitCI = func(in activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		if err := h.checkpointRepositoryStep(in.Step); err != nil {
			return activities.AwaitCIOutput{}, err
		}
		return activities.AwaitCIOutput{CommitSHA: in.CI.CommitSHA, Green: true}, nil
	}
	h.mergeResult = func(in activities.TargetMergePullRequestInput) (work.PullRequestMergeResult, error) {
		if len(h.mergeInputs) == 1 {
			return work.PullRequestMergeResult{Outcome: work.PullRequestMergeHeadChanged, PullRequest: work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: "H2"}}, nil
		}
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeConfirmed, MergeSHA: "M2"}, nil
	}
	h.run(input)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.mergeInputs) != 2 || h.mergeInputs[0].ExpectedHeadSHA != "H1" || h.mergeInputs[1].ExpectedHeadSHA != "H2" {
		t.Fatalf("merge requests = %+v, want only H1 then independently authorized H2", h.mergeInputs)
	}
	reviews := agentChildrenAtStage(h.agentInputs, work.StageReview)
	if len(reviews) != 2 || reviews[0].Attempt.PromptContext.CandidateHeadSHA != "H1" || reviews[1].Attempt.PromptContext.CandidateHeadSHA != "H2" || reviews[0].Seed != nil || reviews[1].Seed != nil || reviews[1].Attempt.Prior.LatestReview.Value() == nil || len(reviews[1].Attempt.Prior.ReviewLedger) != 1 {
		t.Fatalf("review handoffs = %+v, want independent fresh H1 and H2 reviews", reviews)
	}
	detail, err := recorderStore.TargetRunDetail(ctx, input.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if detail.Run.ReviewedHead != "H2" || detail.Run.MergeSHA != "M2" {
		t.Fatalf("terminal run = %+v, want reviewed H2 / M2", detail.Run)
	}
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepMergePullRequest && step.Step.State != work.StepStateCompleted {
			t.Fatalf("feedback merge step = %+v, want completed history rather than a stranded running step", step.Step)
		}
	}
}

// A repository ruleset can be repaired by an operator without changing the
// reviewed candidate. Native activity retry keeps that repair window inside
// the one Merge Step, bounded by the Run's remaining semantic deadline.
func TestWorkOnTicketRetriesRepairableMergeFailuresWithinOneStepUntilSemanticDeadline(t *testing.T) {
	for _, tc := range []struct {
		name         string
		activityType string
	}{
		{name: "ruleset", activityType: activities.ErrTypeRuleset},
		{name: "availability", activityType: activities.ErrTypeTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := storefake.New()
			ticket, err := s.CreateTicket(ctx, "merge repair", "wait for the repairable merge failure", nil)
			if err != nil {
				t.Fatalf("CreateTicket: %v", err)
			}
			in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000025", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
			h := newWorkOnTicketHarness(t, s)
			mergeTries := 0
			var mergeScheduleToClose time.Duration
			h.env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
				if info.ActivityType.Name == "TargetMergePullRequest" {
					mergeScheduleToClose = info.ScheduleToCloseTimeout
				}
			})
			h.mergeResult = func(activities.TargetMergePullRequestInput) (work.PullRequestMergeResult, error) {
				mergeTries++
				if mergeTries == 1 {
					return work.PullRequestMergeResult{}, temporal.NewApplicationError("repairable merge failure", tc.activityType, nil)
				}
				return work.PullRequestMergeResult{Outcome: work.PullRequestMergeConfirmed, MergeSHA: "M1"}, nil
			}

			h.run(in)
			if err := h.env.GetWorkflowError(); err != nil {
				t.Fatalf("WorkOnTicket: %v", err)
			}
			if mergeTries != 2 || len(h.mergeInputs) != 2 || h.mergeInputs[0].ExpectedHeadSHA != "H1" || h.mergeInputs[1].ExpectedHeadSHA != "H1" {
				t.Fatalf("%s retries = %d, merge inputs = %+v; want two exact-H1 attempts", tc.name, mergeTries, h.mergeInputs)
			}
			if mergeScheduleToClose != in.Policy.SemanticDeadline {
				t.Fatalf("merge ScheduleToClose = %s, want remaining semantic deadline %s", mergeScheduleToClose, in.Policy.SemanticDeadline)
			}
			detail, err := s.TargetRunDetail(ctx, in.RunID)
			if err != nil {
				t.Fatalf("TargetRunDetail: %v", err)
			}
			var merges, attempts int
			for _, step := range detail.Steps {
				if step.Step.Kind == work.StepMergePullRequest {
					merges++
				}
				attempts += len(step.Attempts)
			}
			if merges != 1 || attempts != 3 || detail.Run.TargetOutcome != work.RunOutcomeSucceeded {
				t.Fatalf("%s repair result = %+v, want one merge step, three agent attempts, and success", tc.name, detail)
			}
		})
	}
}

func TestWorkOnTicketFinalizesMergeRetryDeadline(t *testing.T) {
	for _, tc := range []struct {
		name         string
		activityType string
		failure      work.RunFailureKind
		result       string
	}{
		{name: "ruleset", activityType: activities.ErrTypeRuleset, failure: work.RunFailureGitHubRuleset, result: `{"kind":"github_ruleset"}`},
		{name: "availability", activityType: activities.ErrTypeTransient, failure: work.RunFailureGitHubUnavailable, result: `{"kind":"github_unavailable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := storefake.New()
			ticket, err := s.CreateTicket(ctx, "merge retry deadline", "terminalize exhausted repair window", nil)
			if err != nil {
				t.Fatalf("CreateTicket: %v", err)
			}
			in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000026", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
			h := newWorkOnTicketHarness(t, s)
			h.mergeResult = func(activities.TargetMergePullRequestInput) (work.PullRequestMergeResult, error) {
				lastFailure := temporal.NewApplicationError("merge remains unavailable", tc.activityType, nil)
				return work.PullRequestMergeResult{}, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE, lastFailure)
			}

			h.run(in)
			var application *temporal.ApplicationError
			if err := h.env.GetWorkflowError(); !errors.As(err, &application) || application.Type() != tc.activityType || !application.NonRetryable() {
				t.Fatalf("WorkOnTicket error = %v, want non-retryable %s", err, tc.activityType)
			}
			got, err := s.Ticket(ctx, ticket.ID)
			if err != nil {
				t.Fatalf("Ticket: %v", err)
			}
			if got.State != store.TicketFailed || got.ActiveRunID != "" {
				t.Fatalf("merge-deadline ticket = %+v, want failed with no active owner", got)
			}
			detail, err := s.TargetRunDetail(ctx, in.RunID)
			if err != nil {
				t.Fatalf("TargetRunDetail: %v", err)
			}
			if detail.Run.TargetOutcome != work.RunOutcomeFailed || detail.Run.TargetFailure != tc.failure {
				t.Fatalf("merge-deadline run = %+v, want failed %s", detail.Run, tc.failure)
			}
			for _, step := range detail.Steps {
				if step.Step.Kind == work.StepMergePullRequest && (step.Step.State != work.StepStateFailed || string(step.Step.Result) != tc.result) {
					t.Fatalf("merge-deadline step = %+v, want failed %s", step.Step, tc.result)
				}
			}
		})
	}
}

// A terminal recording activity can exhaust after the merge retry window
// closes. The deferred finalizer must retry the same classified outcome rather
// than strand ownership or rewrite it as a generic persistence failure.
func TestWorkOnTicketRetriesFailedMergeTerminalRecording(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "merge terminal retry", "persist the classified outcome", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000032", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.mergeResult = func(activities.TargetMergePullRequestInput) (work.PullRequestMergeResult, error) {
		lastFailure := temporal.NewApplicationError("ruleset remains unavailable", activities.ErrTypeRuleset, nil)
		return work.PullRequestMergeResult{}, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE, lastFailure)
	}
	finalizeCalls := 0
	h.env.RegisterActivityWithOptions(
		func(_ context.Context, input store.RunFailureInput) (store.TerminalResult, error) {
			finalizeCalls++
			if finalizeCalls == 1 {
				return store.TerminalResult{}, temporal.NewNonRetryableApplicationError("initial terminal write unavailable", activities.ErrTypePermanent, nil)
			}
			return s.FinalizeRunFailure(context.Background(), input)
		},
		activity.RegisterOptions{Name: "FinalizeRunFailure", DisableAlreadyRegisteredCheck: true},
	)

	h.run(in)
	var application *temporal.ApplicationError
	if err := h.env.GetWorkflowError(); !errors.As(err, &application) || application.Type() != activities.ErrTypeRuleset {
		t.Fatalf("WorkOnTicket error = %v, want original ruleset classification", err)
	}
	if finalizeCalls != 2 {
		t.Fatalf("FinalizeRunFailure calls = %d, want initial write and deferred reconciliation", finalizeCalls)
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if got.State != store.TicketFailed || got.ActiveRunID != "" || detail.Run.TargetOutcome != work.RunOutcomeFailed || detail.Run.TargetFailure != work.RunFailureGitHubRuleset {
		t.Fatalf("reconciled terminal state = ticket %+v / run %+v, want failed GitHub ruleset", got, detail.Run)
	}
	mergeStep := detail.Steps[len(detail.Steps)-1].Step
	if mergeStep.Kind != work.StepMergePullRequest || mergeStep.State != work.StepStateFailed || string(mergeStep.Result) != `{"kind":"github_ruleset"}` {
		t.Fatalf("reconciled merge step = %+v, want failed GitHub ruleset result", mergeStep)
	}
}

func TestWorkOnTicketRetriesPendingCIInsideOneStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "pending CI", "wait", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000003", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.awaitCI = func(input activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		if len(h.ciInputs) == 1 {
			return activities.AwaitCIOutput{}, temporal.NewApplicationErrorWithOptions("checks still pending", activities.ErrTypeCINotConcluded, temporal.ApplicationErrorOptions{NextRetryDelay: 15 * time.Second})
		}
		if err := h.checkpointRepositoryStep(input.Step); err != nil {
			return activities.AwaitCIOutput{}, err
		}
		return activities.AwaitCIOutput{CommitSHA: input.CI.CommitSHA, Green: true}, nil
	}
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	var ciSteps, attempts int
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepAwaitCI {
			ciSteps++
		}
		attempts += len(step.Attempts)
	}
	if len(h.ciInputs) != 2 || ciSteps != 1 || attempts != 3 {
		t.Fatalf("pending CI = %d reads, %d CI steps, %d agent attempts; want 2, 1, 3", len(h.ciInputs), ciSteps, attempts)
	}
}

// A ScheduleToClose timeout means Temporal has exhausted the complete CI
// observation window. It is terminal even when the broader semantic deadline
// still has time left: leaving the await-ci Step running strands the Ticket.
func TestWorkOnTicketFinalizesCIScheduleToCloseTimeoutImmediately(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "CI schedule timeout", "finish durable failure", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	policy := work.DefaultTargetRunPolicy()
	// The test environment returns the server's terminal timeout directly. One
	// activity attempt avoids simulating the full two-hour wall clock while
	// retaining the exact ScheduleToClose timeout classification.
	policy.AwaitCI.Retry.MaximumAttempts = 1
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000023", Policy: policy, CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.awaitCI = func(activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		return activities.AwaitCIOutput{}, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE, nil)
	}

	h.run(in)
	var application *temporal.ApplicationError
	if err := h.env.GetWorkflowError(); !errors.As(err, &application) || application.Type() != activities.ErrTypeCIUnobserved {
		t.Fatalf("WorkOnTicket error = %v, want non-retryable %s", err, activities.ErrTypeCIUnobserved)
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketFailed || got.ActiveRunID != "" {
		t.Fatalf("CI-timeout ticket = %+v, want failed with no active owner", got)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeFailed || detail.Run.TargetFailure != work.RunFailureCIUnobserved {
		t.Fatalf("CI-timeout run = %+v, want failed ci_unobserved", detail.Run)
	}
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepAwaitCI && (step.Step.State != work.StepStateFailed || string(step.Step.Result) != `{"kind":"ci_unobserved"}`) {
			t.Fatalf("CI timeout step = %+v, want failed ci_unobserved result", step.Step)
		}
		if step.Step.Kind == work.StepReview {
			t.Fatalf("steps = %+v, want no post-timeout review", detail.Steps)
		}
	}
}

func TestWorkOnTicketClassifiesAnotherCITimeoutAsInfrastructure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "CI retryable timeout", "do not conflate timeout classes", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	policy := work.DefaultTargetRunPolicy()
	policy.AwaitCI.Retry.MaximumAttempts = 1
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000024", Policy: policy, CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.awaitCI = func(activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		return activities.AwaitCIOutput{}, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil)
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err == nil {
		t.Fatal("WorkOnTicket succeeded after a non-ScheduleToClose timeout")
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketFailed || got.ActiveRunID != "" {
		t.Fatalf("non-ScheduleToClose ticket = %+v, want failed with no active owner", got)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeFailed || detail.Run.TargetFailure != work.RunFailureInfrastructure {
		t.Fatalf("non-ScheduleToClose run = %+v, want failed/infrastructure rather than CI-unobserved", detail.Run)
	}
	lastStep := detail.Steps[len(detail.Steps)-1].Step
	var terminalResult struct {
		Kind        string              `json:"kind"`
		StepKind    work.StepKind       `json:"step_kind"`
		FailureKind work.RunFailureKind `json:"failure_kind"`
	}
	if err := json.Unmarshal(lastStep.Result, &terminalResult); err != nil {
		t.Fatalf("decode terminal Step result: %v", err)
	}
	if lastStep.Kind != work.StepAwaitCI || lastStep.State != work.StepStateFailed || terminalResult.Kind != "terminal_failure" || terminalResult.StepKind != work.StepAwaitCI || terminalResult.FailureKind != work.RunFailureInfrastructure {
		t.Fatalf("terminal Step = %+v result %+v, want failed structured infrastructure result", lastStep, terminalResult)
	}
}

// Credential renewal is supporting machinery for one authorized execution,
// not another Agent Attempt. A long-running implement child must remain the
// same execution while credentials renew at thirty and sixty minutes.
func TestWorkOnTicketRenewsCredentialDuringOneActiveAgentAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "renew credentials", "keep Git authenticated", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000008", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.implementWait = 61 * time.Minute
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}

	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	attempts := 0
	for _, step := range detail.Steps {
		attempts += len(step.Attempts)
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	if attempts != 3 || len(h.rotations) != 5 || len(h.agentInputs) != 3 || len(implements) != 1 {
		t.Fatalf("credential renewal = %d durable attempts, %d rotations, %d children (%d implement); want three attempts, renewals at 30 and 60 minutes, and one uninterrupted implement child", attempts, len(h.rotations), len(h.agentInputs), len(implements))
	}
}

// Model-turn retries belong to AgentWorkflow. WorkOnTicket starts one child for
// an authorized Attempt and passes the immutable retry policy through instead
// of scheduling or retrying a direct agent activity itself.
func TestWorkOnTicketDelegatesModelTurnRetriesToOneChildPerAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "retry agent", "reconcile it", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000009", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	if len(implements) != 1 || implements[0].ModelTurnPolicy != in.Policy.Agent {
		t.Fatalf("implement children = %+v, want one child carrying the immutable model-turn retry policy", implements)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	attempts := 0
	for _, step := range detail.Steps {
		attempts += len(step.Attempts)
	}
	if attempts != 3 || len(h.agentInputs) != 3 || len(h.rotations) != 3 {
		t.Fatalf("child delegation = %d durable attempts, %d children, %d credential lifecycles; want three of each", attempts, len(h.agentInputs), len(h.rotations))
	}
}

// Every authorized durable Attempt owns exactly one bounded child identity.
// The identity is also the child's cache/evidence namespace, so retries and
// artifacts cannot drift onto a different semantic execution.
func TestWorkOnTicketMapsAuthorizedAttemptsToBoundedChildIdentities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "agent scheduling", "schedule the attempt", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000010", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.agentInputs) != 3 || len(h.agentChildIDs) != 3 || len(h.finalizedAttempts) != 3 {
		t.Fatalf("children/evidence = %d/%d/%d, want one child and one finalized Attempt for plan, implement, and review", len(h.agentInputs), len(h.agentChildIDs), len(h.finalizedAttempts))
	}
	for index, input := range h.agentInputs {
		attempt := h.finalizedAttempts[index]
		want := fmt.Sprintf("agent/%s/step/%d/attempt/%d", attempt.RunID, attempt.StepOrdinal, attempt.AttemptNo)
		if input.Identity != want || input.CacheKey != want || h.agentChildIDs[index] != want {
			t.Fatalf("child %d identity = input %q / cache %q / workflow %q, want %q", index, input.Identity, input.CacheKey, h.agentChildIDs[index], want)
		}
	}
}

// The child owns the acceptance retry shape. WorkOnTicket must copy the full
// policy into every child input without expanding ten model-turn tries into
// ten parent-level executions.
func TestWorkOnTicketCarriesAcceptanceBackoffIntoEveryChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "agent retry cap", "bound unavailable model retries", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000013", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.agentInputs) != 3 {
		t.Fatalf("scheduled children = %d, want exactly plan, implement, and review", len(h.agentInputs))
	}
	for _, input := range h.agentInputs {
		if input.ModelTurnPolicy != in.Policy.Agent || input.ModelTurnPolicy.Retry.MaximumAttempts != 10 || input.ModelTurnPolicy.Retry.InitialInterval != 10*time.Second || input.ModelTurnPolicy.Retry.BackoffCoefficient != 2 || input.ModelTurnPolicy.Retry.MaximumInterval != 5*time.Minute {
			t.Fatalf("child model-turn policy = %+v, want acceptance 10s x2 backoff, five-minute cap, ten tries", input.ModelTurnPolicy)
		}
	}
}

// Infrastructure can fail after an Agent Attempt is authorized but before an
// agent activity runs. Terminalization must close both levels as failed so the
// durable history never presents abandoned work as running or completed.
func TestWorkOnTicketFailsRunningAttemptWithItsActiveStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "attempt infrastructure failure", "close active history", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000033", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.rotationErr = temporal.NewNonRetryableApplicationError("credential rotation unavailable", activities.ErrTypePermanent, nil)

	h.run(in)
	if err := h.env.GetWorkflowError(); err == nil {
		t.Fatal("WorkOnTicket succeeded after credential rotation failed")
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	last := detail.Steps[len(detail.Steps)-1]
	if last.Step.Kind != work.StepPlan || last.Step.State != work.StepStateFailed || len(last.Attempts) != 1 || last.Attempts[0].State != work.AgentAttemptFailed || last.Attempts[0].FailureKind != work.RunFailureInfrastructure {
		t.Fatalf("terminal agent history = %+v, want failed plan Step and failed infrastructure Attempt", last)
	}
}

// AgentWorkflow owns the result until its evidence is durable. WorkOnTicket
// must finalize each child result before it completes the enclosing Step.
func TestWorkOnTicketFinalizesChildEvidenceBeforeCompletingAgentStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "persist child evidence", "finalize before completing the step", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000011", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.finalizedAttempts) != 3 {
		t.Fatalf("finalized evidence = %+v, want all three child results", h.finalizedAttempts)
	}
	for index, event := range h.agentPersistenceEvents {
		if event.kind != "finalize" {
			continue
		}
		if index+1 >= len(h.agentPersistenceEvents) || h.agentPersistenceEvents[index+1] != (agentPersistenceEvent{kind: "complete-step", stepOrdinal: event.stepOrdinal}) {
			t.Fatalf("agent persistence events = %+v, want each exact Step completed immediately after its evidence finalization", h.agentPersistenceEvents)
		}
	}
}

// Target evidence and prompt decoding once shared the Go method name Finalize.
// WorkOnTicket must schedule the evidence boundary under its explicit stable
// wire name so worker registration order can never route the evidence payload
// to the prompt decoder.
func TestWorkOnTicketNamesTheTargetEvidenceActivityUnambiguously(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "name target evidence", "record the child result", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000034", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	want := activities.TargetAgentEvidenceFinalizeActivityName
	count := 0
	for _, name := range h.activityNames {
		if name == want {
			count++
		}
		if name == agent.FinalizeActivityName {
			t.Fatalf("target evidence used ambiguous activity name %q; activities = %v", name, h.activityNames)
		}
	}
	if count != 3 {
		t.Fatalf("target evidence activity count = %d, want plan, implement, and review under %q; activities = %v", count, want, h.activityNames)
	}
}

// A classified terminal child failure ends one semantic execution and starts
// one fresh Attempt under the same Step.
func TestWorkOnTicketStartsFreshSemanticAttemptOnlyForClassifiedTerminalChildFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "unresumable attempt", "authorize a replacement", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000012", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	firstImplementIdentity := fmt.Sprintf("agent/%s/step/5/attempt/1", in.RunID)
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.Identity == firstImplementIdentity {
			return workflows.AgentWorkflowResult{Failure: &agent.TerminalFailure{Kind: agent.TerminalFailureInvalidProviderOutcome}}, nil
		}
		return targetAgentWorkflowResult(t, input), nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	var implement store.TargetStepDetail
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepImplement {
			implement = step
			break
		}
	}
	if implement.Step.State != work.StepStateCompleted || len(implement.Attempts) != 2 {
		t.Fatalf("implement step = %+v, want one completed Step with two attempts", implement)
	}
	if first, second := implement.Attempts[0], implement.Attempts[1]; first.ID.AttemptNo != 1 || first.State != work.AgentAttemptFailed || first.FailureKind != work.RunFailureAgentUnrecoverable || second.ID.AttemptNo != 2 {
		t.Fatalf("implement attempts = %+v, want failed unresumable attempt 1 then explicit attempt 2", implement.Attempts)
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	if len(implements) != 2 || implements[0].Identity != firstImplementIdentity || implements[1].Identity != fmt.Sprintf("agent/%s/step/5/attempt/2", in.RunID) || implements[1].Seed != nil {
		t.Fatalf("replacement children = %+v, want failed attempt 1 followed by a fresh unseeded attempt 2", implements)
	}
}

// A failed child can still have a transcript. Its durable evidence must not
// serialize the absent StageOutput before the parent starts the next Attempt.
func TestWorkOnTicketRecordsTerminalChildFailureWithTranscriptThenStartsFreshAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "unresumable attempt with evidence", "retain the failed transcript", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000035", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	firstImplementIdentity := fmt.Sprintf("agent/%s/step/5/attempt/1", in.RunID)
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.Identity == firstImplementIdentity {
			ordinary := targetAgentWorkflowResult(t, input)
			return workflows.AgentWorkflowResult{
				Failure:         &agent.TerminalFailure{Kind: agent.TerminalFailureInvalidProviderOutcome},
				Usage:           ordinary.Usage,
				UsageMeasured:   ordinary.UsageMeasured,
				ConversationRef: ordinary.ConversationRef,
				TranscriptRef:   ordinary.TranscriptRef,
			}, nil
		}
		return targetAgentWorkflowResult(t, input), nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	var implement store.TargetStepDetail
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepImplement {
			implement = step
			break
		}
	}
	if implement.Step.State != work.StepStateCompleted || len(implement.Attempts) != 2 {
		t.Fatalf("implement step = %+v, want one completed Step with two Attempts", implement)
	}
	if first, second := implement.Attempts[0], implement.Attempts[1]; first.ID.AttemptNo != 1 || first.State != work.AgentAttemptFailed || first.FailureKind != work.RunFailureAgentUnrecoverable || second.ID.AttemptNo != 2 || second.State != work.AgentAttemptSucceeded {
		t.Fatalf("implement attempts = %+v, want failed transcript-backed Attempt 1 then succeeded Attempt 2", implement.Attempts)
	}
	if len(h.finalizedAttempts) != 4 || h.finalizedAttempts[1] != (store.TargetAttemptID{RunID: in.RunID, StepOrdinal: 5, AttemptNo: 1}) {
		t.Fatalf("finalized evidence = %+v, want the failed transcript-backed implement Attempt finalized before its replacement", h.finalizedAttempts)
	}
}

// A terminal child failure outside the replacement vocabulary ends the Run
// after recording its one failed Attempt. It must not silently schedule a
// second semantic execution under the same Step.
func TestWorkOnTicketDoesNotReplaceUnclassifiedTerminalChildFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "model exhausted", "do not retry semantically", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000034", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.Attempt.Key.Stage == work.StageImplement {
			return workflows.AgentWorkflowResult{Failure: &agent.TerminalFailure{Kind: agent.TerminalFailureModelExhausted}}, nil
		}
		return targetAgentWorkflowResult(t, input), nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err == nil {
		t.Fatal("WorkOnTicket succeeded after a terminal model failure")
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	if len(implements) != 1 || implements[0].Identity != fmt.Sprintf("agent/%s/step/5/attempt/1", in.RunID) {
		t.Fatalf("implement children = %+v, want exactly one unreplaceable semantic Attempt", implements)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepImplement {
			if len(step.Attempts) != 1 || step.Attempts[0].State != work.AgentAttemptFailed || step.Attempts[0].FailureKind != work.RunFailureAgentUnrecoverable {
				t.Fatalf("implement history = %+v, want one failed terminal Attempt", step)
			}
			return
		}
	}
	t.Fatal("target history did not retain the implement Step")
}

func TestWorkOnTicketContinuesPastFormerAttemptAndReviewCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "unbounded revisions", "keep working until the deadline or a clean review", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000014", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	firstImplementIdentity := fmt.Sprintf("agent/%s/step/5/attempt/1", in.RunID)
	reviews := 0
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.Identity == firstImplementIdentity {
			return workflows.AgentWorkflowResult{Failure: &agent.TerminalFailure{Kind: agent.TerminalFailureInvalidProviderOutcome}}, nil
		}
		if input.Attempt.Key.Stage == work.StageReview {
			reviews++
			if reviews <= 26 {
				return targetBlockingReviewWorkflowResult(t, input), nil
			}
		}
		return targetAgentWorkflowResult(t, input), nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	attempts := 0
	for _, step := range detail.Steps {
		attempts += len(step.Attempts)
	}
	if reviews != 27 || attempts <= 25 || len(h.agentInputs) <= 25 {
		t.Fatalf("reviews=%d durable attempts=%d children=%d, want completion beyond the former 5-review and 25-attempt ceilings", reviews, attempts, len(h.agentInputs))
	}
	if detail.Run.TargetOutcome != work.RunOutcomeSucceeded {
		t.Fatalf("Run outcome = %s, want succeeded", detail.Run.TargetOutcome)
	}
}

// A lost response leaves the worker unable to tell whether the provider
// completed. The replacement must reconcile the same durable Attempt before
// authorizing another one. Here generation two returns the checkpointed output,
// so only generation one represents a provider call.
func TestWorkOnTicketReconcilesSameAttemptAfterLostAgentResponse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "lost session", "recover the active run worker", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000015", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	providerCalls := 0
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.Attempt.Key.Stage == work.StageImplement && input.ToolTarget.RunWorkerIdentity.Generation == 1 {
			providerCalls++
			return targetAgentWorkflowResult(t, input), temporal.NewNonRetryableApplicationError("Run Worker Session lost", activities.ErrTypeRunWorkerSessionLost, nil)
		}
		return targetAgentWorkflowResult(t, input), nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.provisionedInputs) != 2 || h.provisionedInputs[0].Identity.Generation != 1 || h.provisionedInputs[1].Identity.Generation != 2 {
		t.Fatalf("provisioned generations = %+v, want generation one then one replacement", h.provisionedInputs)
	}
	if len(h.deleted) == 0 || h.deleted[0].Identity.Generation != 1 {
		t.Fatalf("deleted workers = %+v, want lost generation one removed before replacement", h.deleted)
	}
	firstDelete, replacementProvision := -1, -1
	for index, operation := range h.controlSequence {
		if operation == "delete:1" && firstDelete == -1 {
			firstDelete = index
		}
		if operation == "provision:2" && replacementProvision == -1 {
			replacementProvision = index
		}
	}
	if firstDelete < 0 || replacementProvision <= firstDelete {
		t.Fatalf("generation lifecycle = %v, want generation one deletion before replacement provision", h.controlSequence)
	}
	if len(h.cloneInputs) != 1 || len(h.restoreInputs) != 1 || h.restoreInputs[0].Branch != h.cloneInputs[0].Step.Branch {
		t.Fatalf("repository replacement restore = clone %+v / restore %+v, want one durable clone Step and one generation-local restore", h.cloneInputs, h.restoreInputs)
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	wantIdentity := fmt.Sprintf("agent/%s/step/5/attempt/1", in.RunID)
	if providerCalls != 1 || len(implements) != 2 || implements[0].Identity != wantIdentity || implements[0].ToolTarget.RunWorkerIdentity.Generation != 1 || implements[1].Identity != wantIdentity || implements[1].ToolTarget.RunWorkerIdentity.Generation != 2 || implements[0].Seed != nil || implements[1].Seed != nil {
		t.Fatalf("implement recovery = calls %d / inputs %+v, want the same unseeded Attempt identity on generation two without another provider call", providerCalls, implements)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if len(detail.Steps) != 10 || detail.Steps[3].Step.Kind != work.StepPlan || len(detail.Steps[3].Attempts) != 1 || len(detail.Steps[4].Attempts) != 1 || detail.Steps[4].Attempts[0].State != work.AgentAttemptSucceeded {
		t.Fatalf("durable recovery detail = %+v, want completed plan once and the same successful implementation Attempt reconciled after loss", detail)
	}
}

func TestWorkOnTicketDoesNotProvisionReplacementUntilLostGenerationIsDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "blocked replacement", "keep one live worker", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000025", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.Attempt.Key.Stage == work.StageImplement {
			return targetAgentWorkflowResult(t, input), temporal.NewNonRetryableApplicationError("Run Worker Session lost", activities.ErrTypeRunWorkerSessionLost, nil)
		}
		return targetAgentWorkflowResult(t, input), nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err == nil {
		t.Fatal("WorkOnTicket succeeded despite failed teardown of the lost generation")
	}
	if len(h.provisionedInputs) != 1 {
		t.Fatalf("provisioned generations = %+v, want no generation two while generation one deletion is unconfirmed", h.provisionedInputs)
	}
}

// Replacement generations serialize, but a later loss after the prior
// recovery completed may advance the same Run again without resetting any
// budget or leaving the preceding generation active.
func TestWorkOnTicketSerializesSequentialSessionReplacements(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "two lost sessions", "recover twice", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000019", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.agentResult = func(input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.Attempt.Key.Stage == work.StageImplement && input.ToolTarget.RunWorkerIdentity.Generation < 3 {
			return targetAgentWorkflowResult(t, input), temporal.NewNonRetryableApplicationError("Run Worker Session lost", activities.ErrTypeRunWorkerSessionLost, nil)
		}
		return targetAgentWorkflowResult(t, input), nil
	}
	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.provisionedInputs) != 3 || h.provisionedInputs[2].Identity.Generation != 3 {
		t.Fatalf("provisioned generations = %+v, want sequential generations one through three", h.provisionedInputs)
	}
	for generation := 1; generation <= 2; generation++ {
		provision, deleted := -1, -1
		for index, operation := range h.controlSequence {
			if operation == "provision:"+strconv.Itoa(generation+1) && provision == -1 {
				provision = index
			}
			if operation == "delete:"+strconv.Itoa(generation) && deleted == -1 {
				deleted = index
			}
		}
		if deleted < 0 || provision <= deleted {
			t.Fatalf("generation %d lifecycle = %v, want delete before next provision", generation, h.controlSequence)
		}
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if len(detail.Steps[4].Attempts) != 1 || detail.Steps[4].Attempts[0].ID.AttemptNo != 1 {
		t.Fatalf("implementation attempts = %+v, want the same reconciled Attempt across sequential Session losses", detail.Steps[4].Attempts)
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	if len(implements) != 3 || implements[0].Identity != implements[1].Identity || implements[1].Identity != implements[2].Identity {
		t.Fatalf("implementation children = %+v, want one Attempt identity replayed across all replacement workers", implements)
	}
}

// Repository-affine operations use the same serialized replacement policy as
// agent execution. Consecutive Session losses may advance generations until
// the absolute deadline, but never leave two workers live or create a new Step.
func TestWorkOnTicketReplacesConsecutiveSessionsDuringRepositoryWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "repository session losses", "recover sync twice", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000029", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.sync = func(input activities.TargetSyncPullRequestInput) (work.PullRequest, error) {
		if h.provisioned.Identity.Generation < 3 {
			return work.PullRequest{}, temporal.NewNonRetryableApplicationError("Run Worker Session lost", activities.ErrTypeRunWorkerSessionLost, nil)
		}
		position := input.Step
		position.PushedHead, position.PullRequestNumber, position.PullRequestNodeID = "H1", 1, "PR_node1"
		if err := h.checkpointRepositoryStep(position); err != nil {
			return work.PullRequest{}, fmt.Errorf("checkpointing recovered pull request: %w", err)
		}
		return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: "H1", Draft: true}, nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.provisionedInputs) != 3 || len(h.restoreInputs) != 2 {
		t.Fatalf("repository recovery = provisions %+v / restores %+v, want generations one through three and two restores", h.provisionedInputs, h.restoreInputs)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	syncSteps := 0
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepSyncPullRequest {
			syncSteps++
		}
	}
	if syncSteps != 1 {
		t.Fatalf("sync steps = %d, want one durable Step across consecutive Session losses", syncSteps)
	}
}

// A replacement Session can itself disappear while restoring the repository.
// Recovery must delete that generation before provisioning the next one and
// retry restoration until one generation is ready for the primary operation.
func TestWorkOnTicketReplacesSessionLostDuringRepositoryRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "restore session loss", "recover the replacement", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000031", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.sync = func(input activities.TargetSyncPullRequestInput) (work.PullRequest, error) {
		if h.provisioned.Identity.Generation == 1 {
			return work.PullRequest{}, temporal.NewNonRetryableApplicationError("Run Worker Session lost", activities.ErrTypeRunWorkerSessionLost, nil)
		}
		position := input.Step
		position.PushedHead, position.PullRequestNumber, position.PullRequestNodeID = "H1", 1, "PR_node1"
		if err := h.checkpointRepositoryStep(position); err != nil {
			return work.PullRequest{}, fmt.Errorf("checkpointing restored pull request: %w", err)
		}
		return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: "H1", Draft: true}, nil
	}
	h.restore = func(activities.RestoreTargetRepositoryInput) error {
		if len(h.restoreInputs) == 1 {
			return temporal.NewNonRetryableApplicationError("replacement Session lost during restore", activities.ErrTypeRunWorkerSessionLost, nil)
		}
		return nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if len(h.provisionedInputs) != 3 || len(h.restoreInputs) != 2 {
		t.Fatalf("restore recovery = provisions %+v / restores %+v, want generations one through three and two restores", h.provisionedInputs, h.restoreInputs)
	}
	wantControl := []string{"provision:1", "delete:1", "provision:2", "delete:2", "provision:3", "delete:3"}
	if fmt.Sprint(h.controlSequence) != fmt.Sprint(wantControl) {
		t.Fatalf("worker control sequence = %v, want serialized %v", h.controlSequence, wantControl)
	}
}

// A durable Git/PR checkpoint is the answer after a lost activity response:
// recovery may invoke the activity again, but it must return the checkpointed
// result instead of repeating the GitHub write or moving back to an older head.
func TestWorkOnTicketRecoversCheckpointedPullRequestAfterSessionLoss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "checkpointed pull request", "do not repeat sync", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000016", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	externalSyncs := 0
	h.sync = func(input activities.TargetSyncPullRequestInput) (work.PullRequest, error) {
		if len(h.syncInputs) == 1 {
			externalSyncs++
			position := input.Step
			position.PushedHead = "H1"
			position.PullRequestNumber, position.PullRequestNodeID = 1, "PR_node1"
			if err := h.checkpointRepositoryStep(position); err != nil {
				return work.PullRequest{}, err
			}
			return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: "H1", Draft: true}, temporal.NewNonRetryableApplicationError("Run Worker Session lost", activities.ErrTypeRunWorkerSessionLost, nil)
		}
		position := input.Step
		position.PushedHead = "H1"
		position.PullRequestNumber, position.PullRequestNodeID = 1, "PR_node1"
		if len(h.syncInputs) > 2 {
			if err := h.checkpointRepositoryStep(position); err != nil {
				return work.PullRequest{}, err
			}
		}
		return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: "H1", Draft: true}, nil
	}
	h.awaitCI = func(input activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		if err := h.checkpointRepositoryStep(input.Step); err != nil {
			return activities.AwaitCIOutput{}, err
		}
		if len(h.ciInputs) == 1 {
			return activities.AwaitCIOutput{CommitSHA: input.CI.CommitSHA, Green: false, RedFailures: []work.CheckFailure{{Name: "test", Evidence: "retry after replacement"}}}, nil
		}
		return activities.AwaitCIOutput{CommitSHA: input.CI.CommitSHA, Green: true}, nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}
	if externalSyncs != 1 || len(h.syncInputs) != 3 {
		t.Fatalf("pull request sync = external %d / calls %d, want one GitHub write, checkpoint recovery, then one later semantic sync", externalSyncs, len(h.syncInputs))
	}
	if len(h.provisionedInputs) != 2 || h.provisionedInputs[1].Identity.Generation != 2 {
		t.Fatalf("recovery provision = %+v, want generation two", h.provisionedInputs)
	}
	implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
	if len(implements) != 2 || implements[1].ToolTarget.RunWorkerIdentity.Generation != 2 || implements[1].Seed == nil || implements[1].Seed.SourceIdentity != implements[0].Identity || implements[1].Seed.ConversationRef != targetAgentWorkflowResult(t, implements[0]).ConversationRef {
		t.Fatalf("post-replacement implementation = %+v, want generation two seeded from the first completed implementation", implements)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepSyncPullRequest && step.Step.State != work.StepStateCompleted {
			t.Fatalf("checkpointed sync Step = %+v, want completed durable effect", step.Step)
		}
	}
}

// A provisioned worker without a Session is not a successful Run. Session
// creation timeout must durably fail the owned Run as unavailable and hand
// that generation to bounded cleanup.
func TestWorkOnTicketCleansUpWorkerWhenSessionCreationTimesOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "session timeout", "clean up the unclaimed worker", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	policy := work.DefaultTargetRunPolicy()
	policy.Provisioning.StartToCloseTimeout = time.Minute
	policy.Provisioning.ScheduleToCloseTimeout = time.Minute
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000017", Policy: policy, CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarnessWithoutSessionWorker(t, s)

	h.run(in)
	if err := h.env.GetWorkflowError(); err == nil {
		t.Fatal("WorkOnTicket succeeded despite no Run Worker Session")
	}
	if len(h.provisionedInputs) != 1 || len(h.deleted) == 0 || h.deleted[0].Identity != h.provisionedInputs[0].Identity {
		t.Fatalf("session-creation cleanup = provision %+v / deletes %+v, want provisioned generation one removed", h.provisionedInputs, h.deleted)
	}
	claimed, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if claimed.State != store.TicketFailed || claimed.ActiveRunID != "" {
		t.Fatalf("ticket after failed Session creation = %+v, want failed with no active owner", claimed)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeFailed || detail.Run.TargetFailure != work.RunFailureRunWorkerUnavailable {
		t.Fatalf("run after failed Session creation = %+v, want failed/run-worker-unavailable", detail.Run)
	}
}

// Cancellation before the ownership claim has committed must leave the
// Ticket untouched. In particular, the disconnected cancellation finalizer
// must not invent a canceled Run that was never admitted.
func TestWorkOnTicketCancellationBeforeClaimLeavesTicketUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "cancel before claim", "do not admit this run", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000020", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.env.RegisterDelayedCallback(func() { h.env.CancelWorkflow() }, 0)

	h.run(in)
	if err := h.env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("WorkOnTicket cancellation error = %v, want cancellation", err)
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketOpen || got.ActiveRunID != "" {
		t.Fatalf("ticket after pre-claim cancellation = %+v, want untouched open ticket", got)
	}
	if _, err := s.TargetRunDetail(ctx, in.RunID); err == nil {
		t.Fatal("TargetRunDetail succeeded for a run canceled before claim")
	}
}

// The claim transaction may commit before Temporal observes a canceled
// activity response. Cancellation reconciles by deterministic Run identity so
// that this narrow race cannot strand the Ticket as active.
func TestWorkOnTicketCancellationAfterClaimCommitBeforeResponseReopensTicket(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "claim response lost", "reconcile cancellation", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000030", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.env.RegisterActivityWithOptions(
		func(_ context.Context, input store.ClaimRunInput) (store.ClaimRunResult, error) {
			result, err := s.ClaimAndStartRun(context.Background(), input)
			if err != nil {
				return store.ClaimRunResult{}, fmt.Errorf("committing claim before response loss: %w", err)
			}
			return result, activity.ErrResultPending
		},
		activity.RegisterOptions{Name: "ClaimAndStartRun", DisableAlreadyRegisteredCheck: true},
	)
	h.env.RegisterDelayedCallback(func() { h.env.CancelWorkflow() }, time.Minute)

	h.run(in)
	if err := h.env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("WorkOnTicket error = %v, want cancellation after committed claim response loss", err)
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketOpen || got.ActiveRunID != "" {
		t.Fatalf("ticket after committed claim response loss = %+v, want reopened with no owner", got)
	}
}

// Cancellation after claim is durable core work: it disconnects from the
// canceled execution, returns only the owned Ticket to open, and tears down
// the Run Worker only after the in-flight child acknowledges cancellation.
func TestWorkOnTicketCancellationDuringAgentReopensTheOwnedTicket(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "cancel active run", "stop during implementation", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000018", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.pendingImplement = true
	h.env.RegisterDelayedCallback(func() { h.env.CancelWorkflow() }, time.Minute)

	h.run(in)
	if err := h.env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("WorkOnTicket cancellation error = %v, want cancellation", err)
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketOpen || got.ActiveRunID != "" {
		t.Fatalf("canceled ticket = %+v, want reopened with no owner", got)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeCanceled || len(h.deleted) != int(in.Policy.Teardown.Retry.MaximumAttempts) || h.deleted[0].Identity.Generation != 1 {
		t.Fatalf("cancellation outcome = run %+v / deletes %+v, want durable cancellation despite bounded teardown failure", detail.Run, h.deleted)
	}
	childCanceled, firstDelete := -1, -1
	for index, operation := range h.controlSequence {
		if operation == "child-canceled" && childCanceled == -1 {
			childCanceled = index
		}
		if operation == "delete:1" && firstDelete == -1 {
			firstDelete = index
		}
	}
	if h.agentChildCanceled != 1 || childCanceled < 0 || firstDelete <= childCanceled {
		t.Fatalf("cancellation sequence = %v (%d child acknowledgements), want WaitForCancellation before worker teardown", h.controlSequence, h.agentChildCanceled)
	}
}

// Once GitHub has confirmed the merge, the terminal Store recording is core
// work. A late workflow cancellation cannot replace that success with a
// canceled outcome, and disconnected teardown still receives its bounded
// cleanup attempt afterward.
func TestWorkOnTicketConfirmedMergeWinsLateCancellation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "late cancellation", "merge is authoritative", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000021", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		if info.ActivityType.Name == "FinalizeConfirmedMerge" {
			h.env.CancelWorkflow()
		}
	})

	h.run(in)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket after confirmed merge: %v", err)
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketDone || got.ActiveRunID != "" {
		t.Fatalf("ticket after confirmed merge = %+v, want durable done terminal", got)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeSucceeded || len(h.deleted) != int(in.Policy.Teardown.Retry.MaximumAttempts) {
		t.Fatalf("late cancellation result = run %+v / deletes %+v, want success with bounded cleanup", detail.Run, h.deleted)
	}
}

// A merge confirmed while replacing a canceled predecessor is success for the
// Ticket but a fence for this workflow execution. The successor must report a
// typed non-success outcome without reopening the Ticket or rewriting either
// Run's durable terminal state.
func TestWorkOnTicketReturnsTypedFenceWhenCanceledPredecessorMerged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "predecessor merged", "fence the successor", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	predecessorRunID := "019fb901-0000-7000-8000-000000000026"
	startedAt := targetTestTime.Add(-time.Hour)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: predecessorRunID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun(predecessor): %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: predecessorRunID, Ordinal: 1, Kind: work.StepSyncPullRequest, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep(sync predecessor): %v", err)
	}
	if _, err := s.CheckpointGitEffect(ctx, store.GitCheckpointInput{
		GitCheckpoint: store.GitCheckpoint{
			RunID: predecessorRunID, StepOrdinal: 1, Branch: "software-factory/predecessor",
			PushedHead: "H-old", PullRequestNumber: 41, PullRequestNodeID: "PR_old",
			StepResult: json.RawMessage(`{"kind":"synced"}`),
		},
		CompletedAt: startedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("CheckpointGitEffect(predecessor): %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: predecessorRunID, Ordinal: 2, Kind: work.StepMergePullRequest, StartedAt: startedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("StartStep(merge predecessor): %v", err)
	}
	if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: predecessorRunID, TicketID: ticket.ID, EndedAt: startedAt.Add(3 * time.Minute)}); err != nil {
		t.Fatalf("CancelRun(predecessor): %v", err)
	}

	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000027", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.cloneResult = func(input activities.CloneTargetRepositoryInput) (activities.CloneTargetRepositoryOutput, error) {
		if input.RetirePullRequestNumber != 41 || input.CarryForwardHead != "H-old" {
			t.Fatalf("predecessor retirement input = %+v, want PR 41 at H-old", input)
		}
		return activities.CloneTargetRepositoryOutput{PredecessorMerge: &work.PullRequestRetirement{Merged: true, ReviewedHead: "H-old", MergeSHA: "M-old"}}, nil
	}

	h.run(in)
	workflowErr := h.env.GetWorkflowError()
	var application *temporal.ApplicationError
	if !errors.As(workflowErr, &application) || application.Type() != activities.ErrTypePredecessorMergeFenced {
		t.Fatalf("WorkOnTicket error = %v, want typed %q fence", workflowErr, activities.ErrTypePredecessorMergeFenced)
	}
	gotTicket, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	predecessor, err := s.TargetRunDetail(ctx, predecessorRunID)
	if err != nil {
		t.Fatalf("TargetRunDetail(predecessor): %v", err)
	}
	successor, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail(successor): %v", err)
	}
	if gotTicket.State != store.TicketDone || gotTicket.ActiveRunID != "" || predecessor.Run.TargetOutcome != work.RunOutcomeSucceeded || predecessor.Run.MergeSHA != "M-old" || successor.Run.TargetOutcome != work.RunOutcomeCanceled {
		t.Fatalf("fenced merge state = ticket %+v / predecessor %+v / successor %+v", gotTicket, predecessor.Run, successor.Run)
	}
}

// The semantic deadline forbids the next primary operation while preserving
// the hard-deadline reserve for durable failed finalization and cleanup.
func TestWorkOnTicketSemanticDeadlineFailsBeforeStartingAnotherStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	ticket, err := s.CreateTicket(ctx, "semantic deadline", "reserve finalization time", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	policy := work.DefaultTargetRunPolicy()
	policy.SemanticDeadline, policy.HardDeadline = time.Minute, 2*time.Minute
	in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000022", Policy: policy, CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
	h := newWorkOnTicketHarness(t, s)
	h.awaitCI = func(input activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
		if len(h.ciInputs) == 1 {
			return activities.AwaitCIOutput{}, temporal.NewApplicationErrorWithOptions("checks pending", activities.ErrTypeCINotConcluded, temporal.ApplicationErrorOptions{NextRetryDelay: 2 * time.Minute})
		}
		if err := h.checkpointRepositoryStep(input.Step); err != nil {
			return activities.AwaitCIOutput{}, err
		}
		return activities.AwaitCIOutput{CommitSHA: input.CI.CommitSHA, Green: true}, nil
	}

	h.run(in)
	if err := h.env.GetWorkflowError(); err == nil {
		t.Fatal("WorkOnTicket succeeded after its semantic deadline")
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketFailed || got.ActiveRunID != "" {
		t.Fatalf("deadline ticket = %+v, want failed with no owner", got)
	}
	detail, err := s.TargetRunDetail(ctx, in.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeFailed || detail.Run.TargetFailure != work.RunFailureSemanticDeadline {
		t.Fatalf("deadline run = %+v, want semantic-deadline failure", detail.Run)
	}
	for _, step := range detail.Steps {
		if step.Step.Kind == work.StepReview {
			t.Fatalf("steps = %+v, want no post-deadline review", detail.Steps)
		}
	}
}

func TestWorkOnTicketRepairsTextConflictOrStaleBaseWithFreshReview(t *testing.T) {
	for _, outcome := range []work.PullRequestMergeOutcome{work.PullRequestMergeTextConflict, work.PullRequestMergeBaseRefreshRequired} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			s := storefake.New()
			ticket, err := s.CreateTicket(ctx, "merge feedback", "repair it", nil)
			if err != nil {
				t.Fatalf("CreateTicket: %v", err)
			}
			in := workflows.WorkOnTicketInput{TicketID: ticket.ID, RunID: "019fb901-0000-7000-8000-000000000006", Policy: work.DefaultTargetRunPolicy(), CloneURL: "https://github.com/example/repository.git", Model: work.Model{Name: "gpt-5", Effort: "high"}}
			h := newWorkOnTicketHarness(t, s)
			h.sync = func(input activities.TargetSyncPullRequestInput) (work.PullRequest, error) {
				head := "H1"
				if len(h.syncInputs) == 2 {
					head = "H2"
				}
				position := input.Step
				position.PushedHead, position.PullRequestNumber, position.PullRequestNodeID = head, 1, "PR_node1"
				if err := h.checkpointRepositoryStep(position); err != nil {
					return work.PullRequest{}, err
				}
				return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: head, Draft: true}, nil
			}
			h.awaitCI = func(input activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
				if err := h.checkpointRepositoryStep(input.Step); err != nil {
					return activities.AwaitCIOutput{}, err
				}
				return activities.AwaitCIOutput{CommitSHA: input.CI.CommitSHA, Green: true}, nil
			}
			h.mergeResult = func(input activities.TargetMergePullRequestInput) (work.PullRequestMergeResult, error) {
				if len(h.mergeInputs) == 1 {
					return work.PullRequestMergeResult{Outcome: outcome, PullRequest: work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: "H1", BaseSHA: "B2"}, Diagnostic: "reconcile the branch"}, nil
				}
				return work.PullRequestMergeResult{Outcome: work.PullRequestMergeConfirmed, MergeSHA: "M2"}, nil
			}
			h.run(in)
			if err := h.env.GetWorkflowError(); err != nil {
				t.Fatalf("WorkOnTicket: %v", err)
			}
			implements := agentChildrenAtStage(h.agentInputs, work.StageImplement)
			reviews := agentChildrenAtStage(h.agentInputs, work.StageReview)
			if len(implements) != 2 || implements[1].Seed == nil || implements[1].Seed.SourceIdentity != implements[0].Identity || implements[1].Attempt.PromptContext.Merge == nil || implements[1].Attempt.PromptContext.Merge.Outcome != outcome || implements[1].Attempt.PromptContext.Merge.CurrentBaseSHA != "B2" || implements[1].Attempt.PromptContext.Merge.Diagnostic != "reconcile the branch" {
				t.Fatalf("merge-feedback implementation = %+v, want seeded typed %s handoff", implements, outcome)
			}
			if len(reviews) != 2 || reviews[0].Attempt.PromptContext.CandidateHeadSHA != "H1" || reviews[1].Attempt.PromptContext.CandidateHeadSHA != "H2" || reviews[0].Seed != nil || reviews[1].Seed != nil {
				t.Fatalf("reviews = %+v, want fresh H1 then H2 reviewers", reviews)
			}
			detail, err := s.TargetRunDetail(ctx, in.RunID)
			if err != nil {
				t.Fatalf("TargetRunDetail: %v", err)
			}
			for _, step := range detail.Steps {
				if step.Step.Kind == work.StepMergePullRequest && step.Step.State != work.StepStateCompleted {
					t.Fatalf("feedback merge step = %+v, want completed history rather than a stranded running step", step.Step)
				}
			}
		})
	}
}

type agentPersistenceEvent struct {
	kind        string
	stepOrdinal int
}

type workOnTicketHarness struct {
	env   *testsuite.TestWorkflowEnvironment
	store workOnTicketStore
	runID string

	provisioned            activities.ProvisionRunWorkerInput
	clone                  activities.CloneTargetRepositoryInput
	provisionedInputs      []activities.ProvisionRunWorkerInput
	cloneInputs            []activities.CloneTargetRepositoryInput
	restoreInputs          []activities.RestoreTargetRepositoryInput
	restore                func(activities.RestoreTargetRepositoryInput) error
	controlSequence        []string
	rotations              []activities.RotateRunWorkerGitHubCredentialInput
	rotationErr            error
	agentInputs            []workflows.AgentWorkflowInput
	agentChildIDs          []string
	agentChildCanceled     int
	agentPersistenceEvents []agentPersistenceEvent
	activityNames          []string
	finalizedAttempts      []store.TargetAttemptID
	ci                     activities.TargetAwaitCIInput
	ready                  activities.TargetMarkPullRequestReadyInput
	merge                  activities.TargetMergePullRequestInput
	mergeInputs            []activities.TargetMergePullRequestInput
	deleted                []activities.DeleteRunWorkerInput
	reviewHead             string
	deleteErr              error
	cloneResult            func(activities.CloneTargetRepositoryInput) (activities.CloneTargetRepositoryOutput, error)

	syncInputs       []activities.TargetSyncPullRequestInput
	ciInputs         []activities.TargetAwaitCIInput
	sync             func(activities.TargetSyncPullRequestInput) (work.PullRequest, error)
	awaitCI          func(activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error)
	mergeResult      func(activities.TargetMergePullRequestInput) (work.PullRequestMergeResult, error)
	agentResult      func(workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error)
	pendingImplement bool
	implementWait    time.Duration
}

type workOnTicketStore interface {
	activities.TargetRunRecorder
	store.CanceledRunRecoveryReader
}

func newWorkOnTicketHarness(t *testing.T, recorderStore workOnTicketStore) *workOnTicketHarness {
	return newWorkOnTicketHarnessWithSessionWorker(t, recorderStore, true)
}

func newWorkOnTicketHarnessWithoutSessionWorker(t *testing.T, recorderStore workOnTicketStore) *workOnTicketHarness {
	return newWorkOnTicketHarnessWithSessionWorker(t, recorderStore, false)
}

func newWorkOnTicketHarnessWithSessionWorker(t *testing.T, recorderStore workOnTicketStore, enableSessionWorker bool) *workOnTicketHarness {
	t.Helper()
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	h := &workOnTicketHarness{env: env, store: recorderStore, deleteErr: errors.New("temporary teardown handoff")}
	env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		h.agentChildIDs = append(h.agentChildIDs, info.WorkflowExecution.ID)
	})
	env.SetOnChildWorkflowCanceledListener(func(*workflow.Info) {
		h.agentChildCanceled++
		h.controlSequence = append(h.controlSequence, "child-canceled")
	})
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, values converter.EncodedValues) {
		h.activityNames = append(h.activityNames, info.ActivityType.Name)
		if info.ActivityType.Name != "CompleteStep" {
			return
		}
		var runID string
		var ordinal int
		var endedAt time.Time
		var result json.RawMessage
		if err := values.Get(&runID, &ordinal, &endedAt, &result); err != nil {
			t.Fatalf("decode CompleteStep input: %v", err)
		}
		h.agentPersistenceEvents = append(h.agentPersistenceEvents, agentPersistenceEvent{kind: "complete-step", stepOrdinal: ordinal})
	})
	if enableSessionWorker {
		env.SetWorkerOptions(worker.Options{
			EnableSessionWorker:               true,
			MaxConcurrentSessionExecutionSize: 1,
		})
	}
	recording, err := activities.NewTargetRecordingActivities(recorderStore)
	if err != nil {
		t.Fatalf("NewTargetRecordingActivities: %v", err)
	}
	env.RegisterActivity(recording)
	recovery, err := activities.NewTargetRecoveryActivities(recorderStore)
	if err != nil {
		t.Fatalf("NewTargetRecoveryActivities: %v", err)
	}
	env.RegisterActivity(recovery)
	env.RegisterActivityWithOptions(func(ctx context.Context, input activities.TargetAgentEvidenceInput) error {
		h.finalizedAttempts = append(h.finalizedAttempts, input.AttemptID)
		result := json.RawMessage(nil)
		if input.State == work.AgentAttemptSucceeded {
			var err error
			result, err = json.Marshal(input.Result)
			if err != nil {
				return err
			}
		}
		usageState := work.UsageUnknown
		if input.UsageMeasured {
			usageState = work.UsageMeasured
		}
		var transcript *store.TargetTranscript
		if input.TranscriptRef.Key != "" {
			transcript = &store.TargetTranscript{CompressedBytes: []byte("test transcript"), Compression: "gzip", UncompressedSizeBytes: 15, Checksum: []byte("test-checksum")}
		}
		_, err := h.store.FinalizeAgentWorkflowAttempt(ctx, store.AgentCheckpointInput{
			ID: input.AttemptID, ExecutionID: input.Identity, State: input.State, FailureKind: input.FailureKind,
			UsageState: usageState, Usage: input.Usage, EndedAt: input.EndedAt,
			Result: result, Transcript: transcript,
		})
		if err == nil {
			h.agentPersistenceEvents = append(h.agentPersistenceEvents, agentPersistenceEvent{kind: "finalize", stepOrdinal: input.AttemptID.StepOrdinal})
		}
		return err
	}, activity.RegisterOptions{Name: activities.TargetAgentEvidenceFinalizeActivityName})

	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		h.agentInputs = append(h.agentInputs, input)
		if input.Attempt.Key.Stage == work.StageReview {
			h.reviewHead = input.Attempt.PromptContext.CandidateHeadSHA
		}
		if h.implementWait > 0 && input.Attempt.Key.Stage == work.StageImplement {
			if err := workflow.NewTimer(ctx, h.implementWait).Get(ctx, nil); err != nil {
				return workflows.AgentWorkflowResult{}, err
			}
		}
		if h.pendingImplement && input.Attempt.Key.Stage == work.StageImplement {
			if err := workflow.Await(ctx, func() bool { return !h.pendingImplement }); err != nil {
				return workflows.AgentWorkflowResult{}, err
			}
		}
		if h.agentResult != nil {
			return h.agentResult(input)
		}
		return targetAgentWorkflowResult(t, input), nil
	}, workflow.RegisterOptions{Name: "AgentWorkflow"})
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.ProvisionRunWorkerInput) (activities.ProvisionRunWorkerOutput, error) {
			h.provisioned = in
			h.provisionedInputs = append(h.provisionedInputs, in)
			h.controlSequence = append(h.controlSequence, "provision:"+strconv.Itoa(in.Identity.Generation))
			id, err := work.RunWorkerName(in.Identity)
			if err != nil {
				return activities.ProvisionRunWorkerOutput{}, err
			}
			return activities.ProvisionRunWorkerOutput{ID: id}, nil
		},
		activity.RegisterOptions{Name: "ProvisionRunWorker"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.CloneTargetRepositoryInput) (activities.CloneTargetRepositoryOutput, error) {
			h.clone = in
			h.cloneInputs = append(h.cloneInputs, in)
			if h.cloneResult != nil {
				return h.cloneResult(in)
			}
			position := in.Step
			position.PushedHead = "B0"
			if err := h.checkpointRepositoryStep(position); err != nil {
				return activities.CloneTargetRepositoryOutput{}, err
			}
			return activities.CloneTargetRepositoryOutput{HeadSHA: "B0"}, nil
		},
		activity.RegisterOptions{Name: "CloneTargetRepository"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.RestoreTargetRepositoryInput) error {
			h.restoreInputs = append(h.restoreInputs, in)
			if h.restore != nil {
				return h.restore(in)
			}
			return nil
		},
		activity.RegisterOptions{Name: "RestoreTargetRepository"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.RotateRunWorkerGitHubCredentialInput) (work.RunWorkerCredentialRevision, error) {
			h.rotations = append(h.rotations, in)
			if h.rotationErr != nil {
				return work.RunWorkerCredentialRevision{}, h.rotationErr
			}
			return work.RunWorkerCredentialRevision{Revision: strconv.Itoa(len(h.rotations)), ExpiresAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}, nil
		},
		activity.RegisterOptions{Name: "RotateRunWorkerGitHubCredential"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.TargetAwaitCIInput) (activities.AwaitCIOutput, error) {
			h.ciInputs = append(h.ciInputs, in)
			if h.awaitCI != nil {
				return h.awaitCI(in)
			}
			h.ci = in
			if err := h.checkpointRepositoryStep(in.Step); err != nil {
				return activities.AwaitCIOutput{}, err
			}
			return activities.AwaitCIOutput{CommitSHA: "H1", Green: true}, nil
		},
		activity.RegisterOptions{Name: "TargetAwaitCI"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.TargetSyncPullRequestInput) (work.PullRequest, error) {
			h.syncInputs = append(h.syncInputs, in)
			if h.sync != nil {
				return h.sync(in)
			}
			position := in.Step
			position.PushedHead = "H1"
			position.PullRequestNumber, position.PullRequestNodeID = 1, "PR_node1"
			if err := h.checkpointRepositoryStep(position); err != nil {
				return work.PullRequest{}, err
			}
			return work.PullRequest{Number: 1, NodeID: "PR_node1", HeadSHA: "H1", Draft: true}, nil
		},
		activity.RegisterOptions{Name: "TargetSyncPullRequest"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.TargetMarkPullRequestReadyInput) error {
			h.ready = in
			return h.checkpointRepositoryStep(in.Step)
		},
		activity.RegisterOptions{Name: "TargetMarkPullRequestReady"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.TargetMergePullRequestInput) (work.PullRequestMergeResult, error) {
			h.merge = in
			h.mergeInputs = append(h.mergeInputs, in)
			if h.mergeResult != nil {
				result, err := h.mergeResult(in)
				if err != nil || result.Outcome == work.PullRequestMergeConfirmed {
					return result, err
				}
				return result, h.checkpointRepositoryStep(in.Step)
			}
			return work.PullRequestMergeResult{Outcome: work.PullRequestMergeConfirmed, MergeSHA: "M1"}, nil
		},
		activity.RegisterOptions{Name: "TargetMergePullRequest"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in activities.DeleteRunWorkerInput) error {
			h.deleted = append(h.deleted, in)
			h.controlSequence = append(h.controlSequence, "delete:"+strconv.Itoa(in.Identity.Generation))
			return h.deleteErr
		},
		activity.RegisterOptions{Name: "DeleteRunWorker"},
	)
	return h
}

func (h *workOnTicketHarness) run(in workflows.WorkOnTicketInput) {
	h.runID = in.RunID
	h.env.ExecuteWorkflow(workflows.WorkOnTicket, in)
}

func (h *workOnTicketHarness) checkpointRepositoryStep(position activities.RepositoryStep) error {
	_, err := h.store.CheckpointGitEffect(context.Background(), store.GitCheckpointInput{
		GitCheckpoint: store.GitCheckpoint{
			RunID: h.runID, StepOrdinal: position.StepOrdinal, Branch: position.Branch,
			PushedHead: position.PushedHead, ObservedBase: position.ObservedBase,
			PullRequestNumber: position.PullRequestNumber, PullRequestNodeID: position.PullRequestNodeID,
			StepResult: json.RawMessage(`{"kind":"fake"}`),
		},
		CompletedAt: targetTestTime,
	})
	return err
}

var targetTestTime = time.Date(2026, 7, 31, 21, 0, 0, 0, time.UTC)

func targetAgentWorkflowResult(t *testing.T, input workflows.AgentWorkflowInput) workflows.AgentWorkflowResult {
	t.Helper()
	var raw string
	switch input.Attempt.Key.Stage {
	case work.StagePlan:
		raw = `{"stage":"plan","value":{"document":"the plan"}}`
	case work.StageImplement:
		raw = `{"stage":"implement","value":{"report":"implemented","blocked":false,"blockedReason":"","title":"target title","body":"target body"}}`
	case work.StageReview:
		raw = `{"stage":"review","value":{"document":"approved","findings":[]}}`
	default:
		t.Fatalf("unknown target agent stage %q", input.Attempt.Key.Stage)
	}
	var result work.StageOutput
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %s output: %v", input.Attempt.Key.Stage, err)
	}
	return workflows.AgentWorkflowResult{
		Result: result, UsageMeasured: true,
		ConversationRef: agent.ConversationRef{Key: input.Identity + "/conversation", Revision: 1, Bytes: 16, Digest: "conversation-digest"},
		TranscriptRef:   agent.TranscriptRef{Key: input.Identity + "/transcript", Revision: 1, Bytes: 14, Digest: "transcript-digest"},
	}
}

func targetBlockingReviewWorkflowResult(t *testing.T, input workflows.AgentWorkflowInput) workflows.AgentWorkflowResult {
	t.Helper()
	var result work.StageOutput
	if err := json.Unmarshal([]byte(`{"stage":"review","value":{"document":"still blocked","findings":[{"id":"same","blocking":true,"summary":"fix it"}]}}`), &result); err != nil {
		t.Fatalf("decode blocking review output: %v", err)
	}
	out := targetAgentWorkflowResult(t, input)
	out.Result = result
	return out
}

func agentChildrenAtStage(inputs []workflows.AgentWorkflowInput, stage work.Stage) []workflows.AgentWorkflowInput {
	children := make([]workflows.AgentWorkflowInput, 0, len(inputs))
	for _, input := range inputs {
		if input.Attempt.Key.Stage == stage {
			children = append(children, input)
		}
	}
	return children
}
