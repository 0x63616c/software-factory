package workflows_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/0x63616c/software-factory/internal/database/databasetest"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	"github.com/google/uuid"
)

// TestWorkOnTicketCommitsRepresentativeHistoryAgainstARealDatabase exercises
// the target workflow's complete happy path through real recording activities.
// It skips without SOFTWARE_FACTORY_DATABASE_URL; CI supplies that database.
func TestWorkOnTicketCommitsRepresentativeHistoryAgainstARealDatabase(t *testing.T) {
	ctx := context.Background()
	s := store.New(databasetest.NewPool(t))
	ticket, err := s.CreateTicket(ctx, "database-backed target run", "complete the representative workflow", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	input := workflows.WorkOnTicketInput{
		TicketID: ticket.ID,
		RunID:    uuid.NewString(),
		Policy:   work.DefaultTargetRunPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	}

	h := newWorkOnTicketHarness(t, s)
	h.deleteErr = nil
	h.run(input)
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("WorkOnTicket: %v", err)
	}

	storedTicket, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, input.RunID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if storedTicket.State != store.TicketDone || storedTicket.ActiveRunID != "" {
		t.Fatalf("terminal ticket = %+v, want atomically done with no active owner", storedTicket)
	}
	if detail.Run.TargetOutcome != work.RunOutcomeSucceeded || detail.Run.ReviewedHead != "H1" || detail.Run.MergeSHA != "M1" {
		t.Fatalf("terminal run = %+v, want confirmed H1/M1 success", detail.Run)
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
	wantAgentStages := map[work.StepKind]work.AgentStage{
		work.StepPlan:      work.AgentStagePlan,
		work.StepImplement: work.AgentStageImplement,
		work.StepReview:    work.AgentStageReview,
	}
	if len(detail.Steps) != len(wantSteps) {
		t.Fatalf("step count = %d, want %d: %+v", len(detail.Steps), len(wantSteps), detail.Steps)
	}
	for index, wantKind := range wantSteps {
		step := detail.Steps[index]
		if step.Step.Ordinal != index+1 || step.Step.Kind != wantKind || step.Step.State != work.StepStateCompleted {
			t.Fatalf("step %d = %+v, want completed %s", index+1, step.Step, wantKind)
		}
		wantStage, agentStep := wantAgentStages[wantKind]
		if !agentStep {
			if len(step.Attempts) != 0 {
				t.Fatalf("infrastructure step %s attempts = %+v, want none", wantKind, step.Attempts)
			}
			continue
		}
		if len(step.Attempts) != 1 {
			t.Fatalf("agent step %s attempts = %+v, want one", wantKind, step.Attempts)
		}
		attempt := step.Attempts[0]
		wantIdentity := fmt.Sprintf("agent/%s/step/%d/attempt/1", input.RunID, step.Step.Ordinal)
		if attempt.AgentStage != wantStage || attempt.State != work.AgentAttemptSucceeded || attempt.ExecutionID != wantIdentity || attempt.UsageState != work.UsageMeasured {
			t.Fatalf("agent step %s attempt = %+v, want successful measured %s evidence", wantKind, attempt, wantStage)
		}
	}
	mergeStep := detail.Steps[len(detail.Steps)-1].Step
	if mergeStep.Kind != work.StepMergePullRequest || mergeStep.State != work.StepStateCompleted {
		t.Fatalf("merge step = %+v, want completed immutable merge evidence", mergeStep)
	}
}
