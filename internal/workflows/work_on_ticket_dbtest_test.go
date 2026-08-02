package workflows_test

import (
	"context"
	"fmt"

	"github.com/0x63616c/software-factory/internal/database/databasetest"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestWorkOnTicketCommitsRepresentativeHistoryAgainstARealDatabase exercises
// the target workflow's complete happy path through real recording activities.
// It skips without SOFTWARE_FACTORY_DATABASE_URL; CI supplies that database.
var _ = Describe("WorkOnTicket DB replay", func() {
	It("commits representative history against a real database", func() {
		t := GinkgoT()
		ctx := context.Background()
		s := store.New(databasetest.NewPool(t))
		ticket, err := s.CreateTicket(ctx, "database-backed target run", "complete the representative workflow", nil)
		Expect(err).NotTo(HaveOccurred(), "CreateTicket")
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
		Expect(h.env.GetWorkflowError()).NotTo(HaveOccurred(), "WorkOnTicket")

		storedTicket, err := s.Ticket(ctx, ticket.ID)
		Expect(err).NotTo(HaveOccurred(), "Ticket")
		detail, err := s.TargetRunDetail(ctx, input.RunID)
		Expect(err).NotTo(HaveOccurred(), "TargetRunDetail")
		Expect(storedTicket.State).To(Equal(store.TicketDone), "terminal ticket state")
		Expect(storedTicket.ActiveRunID).To(BeEmpty(), "no active run after terminal")
		Expect(detail.Run.TargetOutcome).To(Equal(work.RunOutcomeSucceeded))
		Expect(detail.Run.ReviewedHead).To(Equal("H1"))
		Expect(detail.Run.MergeSHA).To(Equal("M1"))

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
		Expect(detail.Steps).To(HaveLen(len(wantSteps)), "step count")
		for index, wantKind := range wantSteps {
			step := detail.Steps[index]
			Expect(step.Step.Ordinal).To(Equal(index+1), "step ordinal")
			Expect(step.Step.Kind).To(Equal(wantKind), "step kind")
			Expect(step.Step.State).To(Equal(work.StepStateCompleted), "step state")
			wantStage, isAgentStep := wantAgentStages[wantKind]
			if !isAgentStep {
				Expect(step.Attempts).To(BeEmpty(), "infrastructure step should not have attempts")
				continue
			}
			Expect(step.Attempts).To(HaveLen(1), "agent step should have one attempt")
			attempt := step.Attempts[0]
			wantIdentity := fmt.Sprintf("agent/%s/step/%d/attempt/1", input.RunID, step.Step.Ordinal)
			Expect(attempt.AgentStage).To(Equal(wantStage))
			Expect(attempt.State).To(Equal(work.AgentAttemptSucceeded))
			Expect(attempt.ExecutionID).To(Equal(wantIdentity))
			Expect(attempt.UsageState).To(Equal(work.UsageMeasured))
		}
		mergeStep := detail.Steps[len(detail.Steps)-1].Step
		Expect(mergeStep.Kind).To(Equal(work.StepMergePullRequest))
		Expect(mergeStep.State).To(Equal(work.StepStateCompleted))
	})
})
