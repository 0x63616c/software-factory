// Package storetest holds reusable behavioral contracts for Store implementations.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// TargetStore is the shared target persistence behavior exercised by the
// PostgreSQL Store and its in-memory fake.
type TargetStore interface {
	store.TicketCreator
	store.TicketReader
	store.TicketStateWriter
	store.WebhookDeliveryAcknowledger
	store.TargetRunClaimer
	store.TargetStepRecorder
	store.TargetAgentRecorder
	store.TargetTerminalRecorder
	TargetRunDetail(context.Context, string) (store.TargetRunDetail, error)
	BindCheckpointCapability(context.Context, store.TargetAttemptID, string) error
	LoadAgentCheckpoint(context.Context, store.TargetAttemptID, string) (store.AgentAttempt, *store.TargetTranscript, bool, error)
	CheckpointGitEffect(context.Context, store.GitCheckpointInput) (store.GitCheckpoint, error)
	BindRepositoryCapability(context.Context, work.RunWorkerIdentity, string) error
	LoadRepositoryCheckpoint(context.Context, work.RunWorkerIdentity, string) (store.GitCheckpoint, bool, error)
	CheckpointRepository(context.Context, store.RepositoryCheckpointInput) (store.GitCheckpoint, error)
	ReconcileAbandonedRun(context.Context, string, store.TicketID) (bool, error)
}

// RunTargetConflictContract verifies that a Store accepts exact retries and
// rejects conflicting terminal evidence in the same way as the real Store.
func RunTargetConflictContract(t *testing.T, newStore func(*testing.T) TargetStore) {
	t.Helper()
	t.Run("zero-step canceled run remains target history", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		ticket, err := s.CreateTicket(ctx, "cancel before first step", "", nil)
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}
		runID := uuid.NewString()
		startedAt := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
		if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
			t.Fatalf("ClaimAndStartRun: %v", err)
		}
		if _, err := s.CancelRun(ctx, store.CancelRunInput{TicketID: ticket.ID, RunID: runID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
			t.Fatalf("CancelRun: %v", err)
		}
		history, err := s.TargetRunDetail(ctx, runID)
		if err != nil {
			t.Fatalf("TargetRunDetail: %v", err)
		}
		if history.Run.TargetOutcome != work.RunOutcomeCanceled || len(history.Steps) != 0 {
			t.Fatalf("TargetRunDetail = %+v, want a zero-Step canceled target Run", history)
		}
	})

	t.Run("done tickets are terminal across every public writer", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		done, err := s.CreateTicket(ctx, "terminal ticket", "", nil)
		if err != nil {
			t.Fatalf("CreateTicket(done): %v", err)
		}
		if _, err := s.UpdateTicketState(ctx, done.ID, store.TicketDone); err != nil {
			t.Fatalf("UpdateTicketState(done): %v", err)
		}
		if _, err := s.UpdateTicketState(ctx, done.ID, store.TicketOpen); err == nil {
			t.Fatal("UpdateTicketState(done -> open) succeeded, want terminal-state rejection")
		}
		if _, err := s.TransitionTicketState(ctx, done.ID, store.TicketDone, store.TicketFailed); err == nil {
			t.Fatal("TransitionTicketState(done -> failed) succeeded, want terminal-state rejection")
		}
		first, err := s.RecordWebhookDelivery(ctx, "terminal-delivery-"+uuid.NewString())
		if err != nil {
			t.Fatalf("RecordWebhookDelivery: %v", err)
		}
		if !first {
			t.Fatal("RecordWebhookDelivery first delivery = false, want true")
		}
		stored, err := s.Ticket(ctx, done.ID)
		if err != nil {
			t.Fatalf("Ticket(done): %v", err)
		}
		if stored.State != store.TicketDone {
			t.Fatalf("Ticket state = %s, want done", stored.State)
		}

		failed, err := s.CreateTicket(ctx, "retryable failure", "", nil)
		if err != nil {
			t.Fatalf("CreateTicket(failed): %v", err)
		}
		if _, err := s.UpdateTicketState(ctx, failed.ID, store.TicketFailed); err != nil {
			t.Fatalf("UpdateTicketState(failed): %v", err)
		}
		if _, err := s.TransitionTicketState(ctx, failed.ID, store.TicketFailed, store.TicketOpen); err != nil {
			t.Fatalf("TransitionTicketState(failed -> open): %v", err)
		}
	})

	t.Run("generic ticket state cannot create target ownership", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		ticket, err := s.CreateTicket(ctx, "target ownership", "", nil)
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}
		if _, err := s.UpdateTicketState(ctx, ticket.ID, store.TicketActive); !errors.Is(err, store.ErrActiveTicketOwnership) {
			t.Fatalf("UpdateTicketState(active) error = %v, want ErrActiveTicketOwnership", err)
		}
		if _, err := s.TransitionTicketState(ctx, ticket.ID, store.TicketOpen, store.TicketActive); !errors.Is(err, store.ErrActiveTicketOwnership) {
			t.Fatalf("TransitionTicketState(active) error = %v, want ErrActiveTicketOwnership", err)
		}
	})

	t.Run("generic ticket state cannot release target ownership", func(t *testing.T) {
		s, ticket, runID, _ := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.UpdateTicketState(ctx, ticket.ID, store.TicketOpen); !errors.Is(err, store.ErrActiveTicketOwnership) {
			t.Fatalf("UpdateTicketState(active -> open) error = %v, want ErrActiveTicketOwnership", err)
		}
		if _, err := s.TransitionTicketState(ctx, ticket.ID, store.TicketActive, store.TicketFailed); !errors.Is(err, store.ErrActiveTicketOwnership) {
			t.Fatalf("TransitionTicketState(active -> failed) error = %v, want ErrActiveTicketOwnership", err)
		}
		stored, err := s.Ticket(ctx, ticket.ID)
		if err != nil {
			t.Fatalf("Ticket(active): %v", err)
		}
		if stored.State != store.TicketActive || stored.ActiveRunID != runID {
			t.Fatalf("Ticket = %+v, want active owner %s", stored, runID)
		}
	})

	t.Run("run identity belongs to exactly one ticket", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		first, err := s.CreateTicket(ctx, "first target owner", "", nil)
		if err != nil {
			t.Fatalf("CreateTicket(first): %v", err)
		}
		second, err := s.CreateTicket(ctx, "second target owner", "", nil)
		if err != nil {
			t.Fatalf("CreateTicket(second): %v", err)
		}
		runID := uuid.NewString()
		startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
		claim := store.ClaimRunInput{TicketID: first.ID, RunID: runID, StartedAt: startedAt}
		if _, err := s.ClaimAndStartRun(ctx, claim); err != nil {
			t.Fatalf("ClaimAndStartRun(first): %v", err)
		}
		if _, err := s.ClaimAndStartRun(ctx, claim); err != nil {
			t.Fatalf("ClaimAndStartRun(exact retry): %v", err)
		}
		if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: second.ID, RunID: runID, StartedAt: startedAt}); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("ClaimAndStartRun(reused run identity) error = %v, want permanent", err)
		}
		stored, err := s.Ticket(ctx, second.ID)
		if err != nil {
			t.Fatalf("Ticket(second): %v", err)
		}
		if stored.State != store.TicketOpen || stored.ActiveRunID != "" {
			t.Fatalf("second Ticket = %+v, want unchanged open Ticket", stored)
		}
	})

	t.Run("agent attempt requires its agent step", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		for _, test := range []struct {
			name  string
			kind  work.StepKind
			stage work.AgentStage
		}{
			{name: "clone repository", kind: work.StepCloneRepository, stage: work.AgentStageImplement},
			{name: "mismatched agent stage", kind: work.StepPlan, stage: work.AgentStageImplement},
		} {
			t.Run(test.name, func(t *testing.T) {
				ordinal := 1
				if test.name == "mismatched agent stage" {
					ordinal = 2
				}
				if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: ordinal, Kind: test.kind, StartedAt: startedAt}); err != nil {
					t.Fatalf("StartStep: %v", err)
				}
				_, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{
					ID:         store.TargetAttemptID{RunID: runID, StepOrdinal: ordinal, AttemptNo: 1},
					AgentStage: test.stage,
					Model:      work.Model{Name: "contract-model", Effort: "medium"},
					UsageState: work.UsageUnknown,
					StartedAt:  startedAt,
				})
				if !errors.Is(err, store.ErrAgentAttemptStep) {
					t.Fatalf("StartAgentAttempt error = %v, want ErrAgentAttemptStep", err)
				}
			})
		}
	})

	t.Run("agent attempt rejects a completed parent step", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepPlan, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		if _, err := s.CompleteStep(ctx, runID, 1, startedAt.Add(time.Minute), []byte(`{"kind":"planned"}`)); err != nil {
			t.Fatalf("CompleteStep: %v", err)
		}
		_, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{
			ID:         store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1},
			AgentStage: work.AgentStagePlan,
			Model:      work.Model{Name: "contract-model", Effort: "medium"},
			UsageState: work.UsageUnknown,
			StartedAt:  startedAt.Add(2 * time.Minute),
		})
		if !errors.Is(err, store.ErrAgentAttemptStep) {
			t.Fatalf("StartAgentAttempt under completed Step error = %v, want ErrAgentAttemptStep", err)
		}

		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 2, Kind: work.StepPlan, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep(retry parent): %v", err)
		}
		retryInput := store.StartAgentAttemptInput{
			ID:         store.TargetAttemptID{RunID: runID, StepOrdinal: 2, AttemptNo: 1},
			AgentStage: work.AgentStagePlan,
			Model:      work.Model{Name: "contract-model", Effort: "medium"},
			UsageState: work.UsageUnknown,
			StartedAt:  startedAt,
		}
		if _, err := s.StartAgentAttempt(ctx, retryInput); err != nil {
			t.Fatalf("StartAgentAttempt(running parent): %v", err)
		}
		if _, err := s.CompleteStep(ctx, runID, 2, startedAt.Add(time.Minute), []byte(`{"kind":"planned"}`)); err != nil {
			t.Fatalf("CompleteStep(retry parent): %v", err)
		}
		if _, err := s.StartAgentAttempt(ctx, retryInput); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("StartAgentAttempt retry under completed Step error = %v, want permanent rejection", err)
		}
	})

	t.Run("step and attempt retries are exact and completion time is immutable", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		stepInput := store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, Iteration: 1, Reason: "first implementation", StartedAt: startedAt}
		if _, err := s.StartStep(ctx, stepInput); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		if _, err := s.StartStep(ctx, stepInput); err != nil {
			t.Fatalf("StartStep(exact retry): %v", err)
		}
		for _, conflict := range []store.StartStepInput{
			{RunID: runID, Ordinal: 1, Kind: work.StepReview, Iteration: 1, Reason: "first implementation", StartedAt: startedAt},
			{RunID: runID, Ordinal: 1, Kind: work.StepImplement, Iteration: 2, Reason: "first implementation", StartedAt: startedAt},
			{RunID: runID, Ordinal: 1, Kind: work.StepImplement, Iteration: 1, Reason: "different reason", StartedAt: startedAt},
			{RunID: runID, Ordinal: 1, Kind: work.StepImplement, Iteration: 1, Reason: "first implementation", StartedAt: startedAt.Add(time.Second)},
		} {
			if _, err := s.StartStep(ctx, conflict); !errors.Is(err, work.ErrPermanent) {
				t.Fatalf("StartStep(conflict %+v) error = %v, want permanent", conflict, err)
			}
		}

		attemptInput := store.StartAgentAttemptInput{ID: store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}
		if _, err := s.StartAgentAttempt(ctx, attemptInput); err != nil {
			t.Fatalf("StartAgentAttempt: %v", err)
		}
		if _, err := s.StartAgentAttempt(ctx, attemptInput); err != nil {
			t.Fatalf("StartAgentAttempt(exact retry): %v", err)
		}
		for _, conflict := range []store.StartAgentAttemptInput{
			{ID: attemptInput.ID, AgentStage: work.AgentStageReview, Model: attemptInput.Model, UsageState: attemptInput.UsageState, StartedAt: attemptInput.StartedAt},
			{ID: attemptInput.ID, AgentStage: attemptInput.AgentStage, Model: work.Model{Name: "other-model", Effort: "medium"}, UsageState: attemptInput.UsageState, StartedAt: attemptInput.StartedAt},
			{ID: attemptInput.ID, AgentStage: attemptInput.AgentStage, Model: work.Model{Name: "contract-model", Effort: "high"}, UsageState: attemptInput.UsageState, StartedAt: attemptInput.StartedAt},
			{ID: attemptInput.ID, AgentStage: attemptInput.AgentStage, Model: attemptInput.Model, UsageState: work.UsageMeasured, StartedAt: attemptInput.StartedAt},
			{ID: attemptInput.ID, AgentStage: attemptInput.AgentStage, Model: attemptInput.Model, UsageState: attemptInput.UsageState, StartedAt: attemptInput.StartedAt.Add(time.Second)},
		} {
			if _, err := s.StartAgentAttempt(ctx, conflict); !errors.Is(err, work.ErrPermanent) {
				t.Fatalf("StartAgentAttempt(conflict %+v) error = %v, want permanent", conflict, err)
			}
		}

		firstEndedAt := startedAt.Add(time.Minute)
		result := []byte(`{"kind":"implemented"}`)
		if _, err := s.CompleteStep(ctx, runID, 1, firstEndedAt, result); err != nil {
			t.Fatalf("CompleteStep: %v", err)
		}
		retry, err := s.CompleteStep(ctx, runID, 1, firstEndedAt.Add(time.Minute), result)
		if err != nil {
			t.Fatalf("CompleteStep(exact retry): %v", err)
		}
		if !retry.EndedAt.Equal(firstEndedAt) {
			t.Fatalf("CompleteStep retry ended_at = %s, want original %s", retry.EndedAt, firstEndedAt)
		}
		if _, err := s.CompleteStep(ctx, runID, 1, firstEndedAt.Add(2*time.Minute), []byte(`{"kind":"different"}`)); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("CompleteStep(conflict) error = %v, want permanent", err)
		}
	})

	t.Run("agent checkpoint persists running progress before terminal evidence", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt: %v", err)
		}
		if err := s.BindCheckpointCapability(ctx, attemptID, "contract-capability"); err != nil {
			t.Fatalf("BindCheckpointCapability: %v", err)
		}
		if _, _, found, err := s.LoadAgentCheckpoint(ctx, attemptID, "contract-capability"); err != nil || found {
			t.Fatalf("LoadAgentCheckpoint(pending) = (found %v, error %v), want authorized pending", found, err)
		}
		if _, _, _, err := s.LoadAgentCheckpoint(ctx, attemptID, "wrong-capability"); !errors.Is(err, store.ErrRunOwnership) {
			t.Fatalf("LoadAgentCheckpoint(wrong capability) error = %v, want ownership", err)
		}
		running := store.AgentCheckpointInput{
			ID: attemptID, Capability: "contract-capability", ExecutionID: "thread-1",
			State: work.AgentAttemptRunning, UsageState: work.UsageMeasured,
			Usage:      work.Usage{InputTokens: 3, CachedInputTokens: 1, OutputTokens: 2, ReasoningTokens: 1},
			Transcript: &store.TargetTranscript{CompressedBytes: []byte("partial transcript"), Compression: "zstd", UncompressedSizeBytes: 18, Checksum: []byte("partial-checksum")},
		}
		if _, err := s.CheckpointAgentAttempt(ctx, running); err != nil {
			t.Fatalf("CheckpointAgentAttempt(running): %v", err)
		}
		if _, err := s.CheckpointAgentAttempt(ctx, running); err != nil {
			t.Fatalf("CheckpointAgentAttempt(running exact retry): %v", err)
		}
		loaded, loadedTranscript, found, err := s.LoadAgentCheckpoint(ctx, attemptID, "contract-capability")
		if err != nil || !found || loaded.State != work.AgentAttemptRunning || loaded.ExecutionID != "thread-1" || loadedTranscript == nil {
			t.Fatalf("LoadAgentCheckpoint(running) = (%+v, %+v, %v, %v)", loaded, loadedTranscript, found, err)
		}
		detail, err := s.TargetRunDetail(ctx, runID)
		if err != nil {
			t.Fatalf("TargetRunDetail(running checkpoint): %v", err)
		}
		if len(detail.Steps) != 1 || len(detail.Steps[0].Attempts) != 1 {
			t.Fatalf("TargetRunDetail(running checkpoint) = %+v, want one Attempt", detail)
		}
		persisted := detail.Steps[0].Attempts[0]
		if persisted.State != work.AgentAttemptRunning || !persisted.EndedAt.IsZero() || persisted.ExecutionID != running.ExecutionID || persisted.Usage != running.Usage || !persisted.TranscriptPresent {
			t.Fatalf("running checkpoint = %+v, want durable non-terminal progress", persisted)
		}
		running.ExecutionID = "different-execution"
		if _, err := s.CheckpointAgentAttempt(ctx, running); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("CheckpointAgentAttempt(running conflict) error = %v, want permanent", err)
		}

		checkpoint := store.AgentCheckpointInput{
			ID: attemptID, Capability: "contract-capability", ExecutionID: "thread-1",
			State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured,
			Usage:   work.Usage{InputTokens: 3, CachedInputTokens: 1, OutputTokens: 2, ReasoningTokens: 1},
			EndedAt: startedAt.Add(time.Minute), Result: []byte(`{"kind":"done"}`),
			Transcript: &store.TargetTranscript{CompressedBytes: []byte("terminal transcript"), Compression: "zstd", UncompressedSizeBytes: 19, Checksum: []byte("terminal-checksum")},
		}
		if _, err := s.CheckpointAgentAttempt(ctx, checkpoint); err != nil {
			t.Fatalf("CheckpointAgentAttempt: %v", err)
		}
		if _, err := s.CheckpointAgentAttempt(ctx, checkpoint); err != nil {
			t.Fatalf("CheckpointAgentAttempt(exact retry): %v", err)
		}
		loaded, loadedTranscript, found, err = s.LoadAgentCheckpoint(ctx, attemptID, "contract-capability")
		if err != nil || !found || loaded.State != work.AgentAttemptSucceeded || !jsonEquivalent(loaded.Result, []byte(`{"kind":"done"}`)) || loadedTranscript == nil {
			t.Fatalf("LoadAgentCheckpoint(succeeded) = (%+v, %+v, %v, %v)", loaded, loadedTranscript, found, err)
		}
		checkpoint.Result = []byte(`{"kind":"different"}`)
		if _, err := s.CheckpointAgentAttempt(ctx, checkpoint); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("CheckpointAgentAttempt(conflict) error = %v, want permanent", err)
		}
		checkpoint.Result = []byte(`{"kind":"done"}`)
		checkpoint.Transcript.Checksum = []byte("different-checksum")
		if _, err := s.CheckpointAgentAttempt(ctx, checkpoint); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("CheckpointAgentAttempt(transcript conflict) error = %v, want permanent", err)
		}
	})

	t.Run("failed before provider identity is still a durable checkpoint", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt: %v", err)
		}
		if err := s.BindCheckpointCapability(ctx, attemptID, "contract-capability"); err != nil {
			t.Fatalf("BindCheckpointCapability: %v", err)
		}
		failed := store.AgentCheckpointInput{ID: attemptID, Capability: "contract-capability", State: work.AgentAttemptFailed, FailureKind: work.RunFailureAgentUnrecoverable, UsageState: work.UsageUnknown, EndedAt: startedAt.Add(time.Minute)}
		if _, err := s.CheckpointAgentAttempt(ctx, failed); err != nil {
			t.Fatalf("CheckpointAgentAttempt(failed before thread): %v", err)
		}
		loaded, _, found, err := s.LoadAgentCheckpoint(ctx, attemptID, "contract-capability")
		if err != nil || !found || loaded.State != work.AgentAttemptFailed || loaded.ExecutionID != "" {
			t.Fatalf("LoadAgentCheckpoint(failed before thread) = (%+v, %v, %v)", loaded, found, err)
		}
	})

	t.Run("main control closes an exhausted attempt before authorizing its replacement", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		first := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: first, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt(first): %v", err)
		}
		failure := store.AgentAttemptFailureInput{ID: first, FailureKind: work.RunFailureAgentUnrecoverable, EndedAt: startedAt.Add(time.Minute)}
		if _, err := s.FailAgentAttempt(ctx, failure); err != nil {
			t.Fatalf("FailAgentAttempt: %v", err)
		}
		if _, err := s.FailAgentAttempt(ctx, failure); err != nil {
			t.Fatalf("FailAgentAttempt(exact retry): %v", err)
		}
		second := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 2}
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: second, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt.Add(time.Minute)}); err != nil {
			t.Fatalf("StartAgentAttempt(replacement): %v", err)
		}
		detail, err := s.TargetRunDetail(ctx, runID)
		if err != nil {
			t.Fatalf("TargetRunDetail: %v", err)
		}
		attempts := detail.Steps[0].Attempts
		if len(attempts) != 2 || attempts[0].State != work.AgentAttemptFailed || attempts[0].FailureKind != work.RunFailureAgentUnrecoverable || attempts[1].ID.AttemptNo != 2 || attempts[1].State != work.AgentAttemptRunning {
			t.Fatalf("attempt history = %+v, want failed attempt 1 then authorized attempt 2", attempts)
		}
	})
	t.Run("failed agent checkpoint preserves running transcript", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt: %v", err)
		}
		if err := s.BindCheckpointCapability(ctx, attemptID, "contract-capability"); err != nil {
			t.Fatalf("BindCheckpointCapability: %v", err)
		}
		usage := work.Usage{InputTokens: 3, CachedInputTokens: 1, OutputTokens: 2, ReasoningTokens: 1}
		running := store.AgentCheckpointInput{
			ID: attemptID, Capability: "contract-capability", ExecutionID: "thread-1",
			State: work.AgentAttemptRunning, UsageState: work.UsageMeasured, Usage: usage,
			Transcript: &store.TargetTranscript{CompressedBytes: []byte("partial transcript"), Compression: "zstd", UncompressedSizeBytes: 18, Checksum: []byte("partial-checksum")},
		}
		if _, err := s.CheckpointAgentAttempt(ctx, running); err != nil {
			t.Fatalf("CheckpointAgentAttempt(running): %v", err)
		}
		failed := store.AgentCheckpointInput{
			ID: attemptID, Capability: "contract-capability", ExecutionID: "thread-1",
			State: work.AgentAttemptFailed, FailureKind: work.RunFailureAgentUnrecoverable,
			UsageState: work.UsageMeasured, Usage: usage, EndedAt: startedAt.Add(time.Minute),
		}
		if _, err := s.CheckpointAgentAttempt(ctx, failed); err != nil {
			t.Fatalf("CheckpointAgentAttempt(failed): %v", err)
		}
		if _, err := s.CheckpointAgentAttempt(ctx, failed); err != nil {
			t.Fatalf("CheckpointAgentAttempt(failed exact retry): %v", err)
		}
		detail, err := s.TargetRunDetail(ctx, runID)
		if err != nil {
			t.Fatalf("TargetRunDetail: %v", err)
		}
		attempt := detail.Steps[0].Attempts[0]
		if attempt.State != work.AgentAttemptFailed || !attempt.TranscriptPresent || !attempt.EndedAt.Equal(failed.EndedAt) {
			t.Fatalf("failed checkpoint = %+v, want terminal failure retaining partial transcript", attempt)
		}
	})

	t.Run("canceled owner cannot checkpoint an agent attempt", func(t *testing.T) {
		s, ticket, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt: %v", err)
		}
		if err := s.BindCheckpointCapability(ctx, attemptID, "contract-capability"); err != nil {
			t.Fatalf("BindCheckpointCapability: %v", err)
		}
		if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
			t.Fatalf("CancelRun: %v", err)
		}
		_, err := s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: attemptID, Capability: "contract-capability", ExecutionID: "thread-1", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, EndedAt: startedAt.Add(2 * time.Minute), Result: []byte(`{"kind":"done"}`), Transcript: &store.TargetTranscript{CompressedBytes: []byte("transcript"), Compression: "zstd", UncompressedSizeBytes: 10, Checksum: []byte("checksum")}})
		if !errors.Is(err, store.ErrRunOwnership) {
			t.Fatalf("CheckpointAgentAttempt after cancellation error = %v, want ErrRunOwnership", err)
		}
	})

	t.Run("canceling an absent claim has one typed outcome", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		ticket, err := s.CreateTicket(ctx, "missing claim", "", nil)
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}
		_, err = s.CancelRun(ctx, store.CancelRunInput{RunID: uuid.NewString(), TicketID: ticket.ID, EndedAt: time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)})
		if !errors.Is(err, store.ErrNoOwnedClaim) {
			t.Fatalf("CancelRun(absent claim) error = %v, want ErrNoOwnedClaim", err)
		}
	})

	t.Run("git checkpoint pull request", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepSyncPullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		checkpoint := store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{RunID: runID, StepOrdinal: 1, Branch: "factory/contract", PushedHead: "head-1", ObservedBase: "base-1", PullRequestNumber: 7, PullRequestNodeID: "node-7", StepResult: []byte(`{"kind":"synced"}`)}, CompletedAt: startedAt.Add(time.Minute)}
		if _, err := s.CheckpointGitEffect(ctx, checkpoint); err != nil {
			t.Fatalf("CheckpointGitEffect: %v", err)
		}
		firstCompletedAt := checkpoint.CompletedAt
		checkpoint.CompletedAt = checkpoint.CompletedAt.Add(time.Minute)
		if _, err := s.CheckpointGitEffect(ctx, checkpoint); err != nil {
			t.Fatalf("CheckpointGitEffect(exact retry): %v", err)
		}
		detail, err := s.TargetRunDetail(ctx, runID)
		if err != nil {
			t.Fatalf("TargetRunDetail: %v", err)
		}
		if len(detail.Steps) != 1 || !detail.Steps[0].Step.EndedAt.Equal(firstCompletedAt) {
			t.Fatalf("Git checkpoint retry Step = %+v, want original ended_at %s", detail.Steps, firstCompletedAt)
		}
		checkpoint.PullRequestNumber = 8
		if _, err := s.CheckpointGitEffect(ctx, checkpoint); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("CheckpointGitEffect(conflict) error = %v, want permanent", err)
		}
	})

	t.Run("repository capability is generation scoped and checkpoint retries are atomic", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		generationOne := work.RunWorkerIdentity{RunID: runID, Generation: 1}
		generationTwo := work.RunWorkerIdentity{RunID: runID, Generation: 2}
		if err := s.BindRepositoryCapability(ctx, generationOne, "repository-one"); err != nil {
			t.Fatalf("BindRepositoryCapability: %v", err)
		}
		if err := s.BindRepositoryCapability(ctx, generationOne, "repository-one"); err != nil {
			t.Fatalf("BindRepositoryCapability(exact retry): %v", err)
		}
		if _, found, err := s.LoadRepositoryCheckpoint(ctx, generationOne, "repository-one"); err != nil || found {
			t.Fatalf("LoadRepositoryCheckpoint(missing) = found %t, error %v", found, err)
		}
		if err := s.BindRepositoryCapability(ctx, generationOne, "conflicting"); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("BindRepositoryCapability(conflict) error = %v, want permanent", err)
		}
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepSyncPullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		input := store.RepositoryCheckpointInput{
			Identity: generationOne, Capability: "repository-one", CompletedAt: startedAt.Add(time.Minute),
			GitCheckpoint: store.GitCheckpoint{RunID: runID, StepOrdinal: 1, Branch: "factory/contract", PushedHead: "head-1", ObservedBase: "base-1", PullRequestNumber: 7, PullRequestNodeID: "node-7", StepResult: []byte(`{"kind":"synced"}`)},
		}
		got, err := s.CheckpointRepository(ctx, input)
		if err != nil {
			t.Fatalf("CheckpointRepository: %v", err)
		}
		input.CompletedAt = input.CompletedAt.Add(time.Minute)
		if retry, err := s.CheckpointRepository(ctx, input); err != nil || retry.PushedHead != got.PushedHead {
			t.Fatalf("CheckpointRepository(exact retry) = %+v, error %v", retry, err)
		}
		detail, err := s.TargetRunDetail(ctx, runID)
		if err != nil {
			t.Fatalf("TargetRunDetail: %v", err)
		}
		if len(detail.Steps) != 1 || detail.Steps[0].Step.State != work.StepStateCompleted || !detail.Steps[0].Step.EndedAt.Equal(startedAt.Add(time.Minute)) {
			t.Fatalf("checkpoint did not atomically preserve completed Step: %+v", detail.Steps)
		}
		loaded, found, err := s.LoadRepositoryCheckpoint(ctx, generationOne, "repository-one")
		if err != nil || !found || loaded.PushedHead != "head-1" {
			t.Fatalf("LoadRepositoryCheckpoint = %+v, found %t, error %v", loaded, found, err)
		}
		if err := s.BindRepositoryCapability(ctx, generationTwo, "repository-two"); err != nil {
			t.Fatalf("BindRepositoryCapability(rotation): %v", err)
		}
		if _, _, err := s.LoadRepositoryCheckpoint(ctx, generationOne, "repository-one"); !errors.Is(err, store.ErrRunOwnership) {
			t.Fatalf("LoadRepositoryCheckpoint(obsolete generation) error = %v, want ownership", err)
		}
		if err := s.BindRepositoryCapability(ctx, generationOne, "repository-one"); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("BindRepositoryCapability(regression) error = %v, want permanent", err)
		}
	})

	t.Run("repository capability cannot cross Runs", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		_, _, firstRunID, startedAt := claimedRun(t, s)
		_, _, secondRunID, _ := claimedRun(t, s)
		first := work.RunWorkerIdentity{RunID: firstRunID, Generation: 1}
		second := work.RunWorkerIdentity{RunID: secondRunID, Generation: 1}
		if err := s.BindRepositoryCapability(ctx, first, "first-repository"); err != nil {
			t.Fatalf("BindRepositoryCapability(first): %v", err)
		}
		if _, found, err := s.LoadRepositoryCheckpoint(ctx, second, "first-repository"); !errors.Is(err, store.ErrRunOwnership) || found {
			t.Fatalf("LoadRepositoryCheckpoint(foreign) = found %t, error %v, want ownership", found, err)
		}
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: secondRunID, Ordinal: 1, Kind: work.StepSyncPullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep(second): %v", err)
		}
		_, err := s.CheckpointRepository(ctx, store.RepositoryCheckpointInput{
			Identity: second, Capability: "first-repository", CompletedAt: startedAt.Add(time.Minute),
			GitCheckpoint: store.GitCheckpoint{RunID: secondRunID, StepOrdinal: 1, Branch: "factory/second", PushedHead: "head", ObservedBase: "base", PullRequestNumber: 2, PullRequestNodeID: "node-2", StepResult: []byte(`{"kind":"synced"}`)},
		})
		if !errors.Is(err, store.ErrRunOwnership) {
			t.Fatalf("CheckpointRepository(foreign) error = %v, want ownership", err)
		}
	})

	for _, terminal := range []string{"canceled", "done"} {
		terminal := terminal
		t.Run("repository capability fences "+terminal+" owner", func(t *testing.T) {
			s, ticket, runID, startedAt := claimedRun(t, newStore(t))
			ctx := context.Background()
			identity := work.RunWorkerIdentity{RunID: runID, Generation: 1}
			if err := s.BindRepositoryCapability(ctx, identity, "repository"); err != nil {
				t.Fatalf("BindRepositoryCapability: %v", err)
			}
			if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepSyncPullRequest, StartedAt: startedAt}); err != nil {
				t.Fatalf("StartStep(repository): %v", err)
			}
			switch terminal {
			case "canceled":
				if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
					t.Fatalf("CancelRun: %v", err)
				}
			case "done":
				if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 2, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
					t.Fatalf("StartStep(merge): %v", err)
				}
				if _, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: ticket.ID, StepOrdinal: 2, ReviewedHead: "head", MergeSHA: "merge", EndedAt: startedAt.Add(time.Minute)}); err != nil {
					t.Fatalf("FinalizeConfirmedMerge: %v", err)
				}
			}
			if _, _, err := s.LoadRepositoryCheckpoint(ctx, identity, "repository"); !errors.Is(err, store.ErrRunOwnership) {
				t.Fatalf("LoadRepositoryCheckpoint(%s) error = %v, want ownership", terminal, err)
			}
			if err := s.BindRepositoryCapability(ctx, work.RunWorkerIdentity{RunID: runID, Generation: 2}, "replacement"); !errors.Is(err, store.ErrRunOwnership) {
				t.Fatalf("BindRepositoryCapability(%s) error = %v, want ownership", terminal, err)
			}
			_, err := s.CheckpointRepository(ctx, store.RepositoryCheckpointInput{
				Identity: identity, Capability: "repository", CompletedAt: startedAt.Add(2 * time.Minute),
				GitCheckpoint: store.GitCheckpoint{RunID: runID, StepOrdinal: 1, Branch: "factory/terminal", PushedHead: "head", ObservedBase: "base", PullRequestNumber: 1, PullRequestNodeID: "node", StepResult: []byte(`{"kind":"synced"}`)},
			})
			if !errors.Is(err, store.ErrRunOwnership) {
				t.Fatalf("CheckpointRepository(%s) error = %v, want ownership", terminal, err)
			}
		})
	}

	t.Run("git checkpoint requires its owned running step", func(t *testing.T) {
		s, _, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		checkpoint := store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{RunID: runID, StepOrdinal: 1, Branch: "factory/contract", PushedHead: "head-1", ObservedBase: "base-1", PullRequestNumber: 7, PullRequestNodeID: "node-7", StepResult: []byte(`{"kind":"synced"}`)}, CompletedAt: startedAt.Add(time.Minute)}
		if _, err := s.CheckpointGitEffect(ctx, checkpoint); err == nil {
			t.Fatal("CheckpointGitEffect without Step succeeded")
		}
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepSyncPullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		if _, err := s.CompleteStep(ctx, runID, 1, startedAt.Add(30*time.Second), []byte(`{"kind":"other"}`)); err != nil {
			t.Fatalf("CompleteStep: %v", err)
		}
		if _, err := s.CheckpointGitEffect(ctx, checkpoint); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("CheckpointGitEffect for completed Step error = %v, want permanent", err)
		}
		checkpoint.StepOrdinal = 2
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 2, Kind: work.StepSyncPullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep(same result): %v", err)
		}
		if _, err := s.CompleteStep(ctx, runID, 2, startedAt.Add(30*time.Second), checkpoint.StepResult); err != nil {
			t.Fatalf("CompleteStep(same result): %v", err)
		}
		if _, err := s.CheckpointGitEffect(ctx, checkpoint); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("CheckpointGitEffect for pre-completed matching Step error = %v, want permanent", err)
		}
	})

	t.Run("confirmed merge", func(t *testing.T) {
		s, ticket, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		terminal := store.ConfirmedMergeInput{RunID: runID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "head-1", MergeSHA: "merge-1", EndedAt: startedAt.Add(time.Minute)}
		if _, err := s.FinalizeConfirmedMerge(ctx, terminal); err != nil {
			t.Fatalf("FinalizeConfirmedMerge: %v", err)
		}
		if _, err := s.FinalizeConfirmedMerge(ctx, terminal); err != nil {
			t.Fatalf("FinalizeConfirmedMerge(exact retry): %v", err)
		}
		terminal.MergeSHA = "merge-2"
		if _, err := s.FinalizeConfirmedMerge(ctx, terminal); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("FinalizeConfirmedMerge(conflict) error = %v, want permanent", err)
		}
	})

	t.Run("late failure cannot reverse confirmed merge", func(t *testing.T) {
		s, ticket, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		if _, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "head-1", MergeSHA: "merge-1", EndedAt: startedAt.Add(time.Minute)}); err != nil {
			t.Fatalf("FinalizeConfirmedMerge: %v", err)
		}
		result, err := s.FinalizeRunFailure(ctx, store.RunFailureInput{RunID: runID, TicketID: ticket.ID, Outcome: work.RunOutcomeFailed, FailureKind: work.RunFailureInfrastructure, EndedAt: startedAt.Add(2 * time.Minute)})
		if err != nil {
			t.Fatalf("FinalizeRunFailure(after merge): %v", err)
		}
		if result.Run.TargetOutcome != work.RunOutcomeSucceeded || result.Run.ReviewedHead != "head-1" || result.Run.MergeSHA != "merge-1" || result.Ticket.State != store.TicketDone {
			t.Fatalf("late failure result = %+v, want unchanged confirmed merge", result)
		}
	})

	t.Run("late failure cannot reverse cancellation", func(t *testing.T) {
		s, ticket, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
			t.Fatalf("CancelRun: %v", err)
		}
		result, err := s.FinalizeRunFailure(ctx, store.RunFailureInput{RunID: runID, TicketID: ticket.ID, Outcome: work.RunOutcomeFailed, FailureKind: work.RunFailureInfrastructure, EndedAt: startedAt.Add(2 * time.Minute)})
		if err != nil {
			t.Fatalf("FinalizeRunFailure(after cancellation): %v", err)
		}
		if result.Run.TargetOutcome != work.RunOutcomeCanceled || result.Ticket.State != store.TicketOpen || result.Ticket.ActiveRunID != "" {
			t.Fatalf("late failure result = %+v, want unchanged cancellation", result)
		}
	})

	t.Run("confirmed merge fences successor owner", func(t *testing.T) {
		s, ticket, firstRunID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: firstRunID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep(first merge): %v", err)
		}
		if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: firstRunID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
			t.Fatalf("CancelRun(first): %v", err)
		}
		secondRunID := uuid.NewString()
		if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: secondRunID, StartedAt: startedAt.Add(2 * time.Minute)}); err != nil {
			t.Fatalf("ClaimAndStartRun(second): %v", err)
		}
		secondStep := store.StartStepInput{RunID: secondRunID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt.Add(2 * time.Minute)}
		if _, err := s.StartStep(ctx, secondStep); err != nil {
			t.Fatalf("StartStep(second implement): %v", err)
		}
		secondAttempt := store.StartAgentAttemptInput{ID: store.TargetAttemptID{RunID: secondRunID, StepOrdinal: 1, AttemptNo: 1}, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "contract-model", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt.Add(2 * time.Minute)}
		if _, err := s.StartAgentAttempt(ctx, secondAttempt); err != nil {
			t.Fatalf("StartAgentAttempt(second): %v", err)
		}
		if err := s.BindCheckpointCapability(ctx, secondAttempt.ID, "second-capability"); err != nil {
			t.Fatalf("BindCheckpointCapability(second): %v", err)
		}
		secondGitStep := store.StartStepInput{RunID: secondRunID, Ordinal: 2, Kind: work.StepSyncPullRequest, StartedAt: startedAt.Add(2 * time.Minute)}
		if _, err := s.StartStep(ctx, secondGitStep); err != nil {
			t.Fatalf("StartStep(second git): %v", err)
		}
		secondGit := store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{RunID: secondRunID, StepOrdinal: 2, Branch: "factory/second", PushedHead: "second-head", ObservedBase: "base", PullRequestNumber: 2, PullRequestNodeID: "node-2", StepResult: []byte(`{"kind":"synced"}`)}, CompletedAt: startedAt.Add(2 * time.Minute)}
		if _, err := s.CheckpointGitEffect(ctx, secondGit); err != nil {
			t.Fatalf("CheckpointGitEffect(second): %v", err)
		}
		secondMergeStep := store.StartStepInput{RunID: secondRunID, Ordinal: 3, Kind: work.StepMergePullRequest, StartedAt: startedAt.Add(2 * time.Minute)}
		if _, err := s.StartStep(ctx, secondMergeStep); err != nil {
			t.Fatalf("StartStep(second merge): %v", err)
		}
		result, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: firstRunID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "first-head", MergeSHA: "first-merge", EndedAt: startedAt.Add(3 * time.Minute)})
		if err != nil {
			t.Fatalf("FinalizeConfirmedMerge(first): %v", err)
		}
		second, err := s.TargetRunDetail(ctx, secondRunID)
		if err != nil {
			t.Fatalf("TargetRunDetail(second): %v", err)
		}
		if result.Ticket.State != store.TicketDone || result.Ticket.ActiveRunID != "" || second.Run.TargetOutcome != work.RunOutcomeCanceled {
			t.Fatalf("successor fence = result %+v, second %+v; want done Ticket and canceled successor", result, second.Run)
		}
		assertOwnership := func(operation string, err error) {
			t.Helper()
			if !errors.Is(err, store.ErrRunOwnership) {
				t.Fatalf("%s after fence error = %v, want ErrRunOwnership", operation, err)
			}
		}
		_, err = s.StartStep(ctx, store.StartStepInput{RunID: secondRunID, Ordinal: 4, Kind: work.StepPlan, StartedAt: startedAt.Add(4 * time.Minute)})
		assertOwnership("StartStep", err)
		_, err = s.CompleteStep(ctx, secondRunID, 1, startedAt.Add(4*time.Minute), []byte(`{"kind":"implemented"}`))
		assertOwnership("CompleteStep", err)
		secondAttempt.ID.AttemptNo = 2
		_, err = s.StartAgentAttempt(ctx, secondAttempt)
		assertOwnership("StartAgentAttempt", err)
		assertOwnership("BindCheckpointCapability", s.BindCheckpointCapability(ctx, store.TargetAttemptID{RunID: secondRunID, StepOrdinal: 1, AttemptNo: 1}, "second-capability"))
		_, err = s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: store.TargetAttemptID{RunID: secondRunID, StepOrdinal: 1, AttemptNo: 1}, Capability: "second-capability", ExecutionID: "second-thread", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, EndedAt: startedAt.Add(4 * time.Minute), Result: []byte(`{"kind":"done"}`), Transcript: &store.TargetTranscript{CompressedBytes: []byte("transcript"), Compression: "zstd", UncompressedSizeBytes: 10, Checksum: []byte("checksum")}})
		assertOwnership("CheckpointAgentAttempt", err)
		_, err = s.CheckpointGitEffect(ctx, secondGit)
		assertOwnership("CheckpointGitEffect", err)
		_, err = s.CancelRun(ctx, store.CancelRunInput{RunID: secondRunID, TicketID: ticket.ID, EndedAt: startedAt.Add(4 * time.Minute)})
		assertOwnership("CancelRun", err)
		_, err = s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: secondRunID, TicketID: ticket.ID, StepOrdinal: 3, ReviewedHead: "second-head", MergeSHA: "second-merge", EndedAt: startedAt.Add(4 * time.Minute)})
		assertOwnership("FinalizeConfirmedMerge", err)
		_, err = s.ReconcileAbandonedRun(ctx, secondRunID, ticket.ID)
		assertOwnership("ReconcileAbandonedRun", err)
	})

	t.Run("confirmed merge requires merge step", func(t *testing.T) {
		s, ticket, runID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		terminal := store.ConfirmedMergeInput{RunID: runID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "head-1", MergeSHA: "merge-1", EndedAt: startedAt.Add(time.Minute)}
		if _, err := s.FinalizeConfirmedMerge(ctx, terminal); !errors.Is(err, store.ErrMergeStep) {
			t.Fatalf("FinalizeConfirmedMerge without step error = %v, want ErrMergeStep", err)
		}
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepAwaitCI, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		if _, err := s.FinalizeConfirmedMerge(ctx, terminal); !errors.Is(err, store.ErrMergeStep) {
			t.Fatalf("FinalizeConfirmedMerge with non-merge step error = %v, want ErrMergeStep", err)
		}
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 2, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep(merge): %v", err)
		}
		terminal.StepOrdinal = 2
		if _, err := s.FinalizeConfirmedMerge(ctx, terminal); err != nil {
			t.Fatalf("FinalizeConfirmedMerge: %v", err)
		}
		detail, err := s.TargetRunDetail(ctx, runID)
		if err != nil {
			t.Fatalf("TargetRunDetail: %v", err)
		}
		if len(detail.Steps) != 2 || detail.Steps[1].Step.State != work.StepStateCompleted {
			t.Fatalf("Merge Step = %+v, want completed", detail.Steps)
		}
	})

	t.Run("maintenance stale owner cannot reopen replacement", func(t *testing.T) {
		s, ticket, abandonedRunID, startedAt := claimedRun(t, newStore(t))
		ctx := context.Background()
		if reopened, err := s.ReconcileAbandonedRun(ctx, abandonedRunID, ticket.ID); err != nil || !reopened {
			t.Fatalf("ReconcileAbandonedRun(initial) = (%t, %v), want (true, nil)", reopened, err)
		}
		replacementRunID := uuid.NewString()
		if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: replacementRunID, StartedAt: startedAt.Add(time.Minute)}); err != nil {
			t.Fatalf("ClaimAndStartRun(replacement): %v", err)
		}
		reopened, err := s.ReconcileAbandonedRun(ctx, abandonedRunID, ticket.ID)
		if err != nil {
			t.Fatalf("ReconcileAbandonedRun(stale): %v", err)
		}
		if reopened {
			t.Fatal("ReconcileAbandonedRun(stale) reopened replacement-owned Ticket")
		}
		current, err := s.Ticket(ctx, ticket.ID)
		if err != nil {
			t.Fatalf("Ticket: %v", err)
		}
		if current.State != store.TicketActive || current.ActiveRunID != replacementRunID {
			t.Fatalf("replacement ownership = %+v, want active owner %q", current, replacementRunID)
		}
	})
}

func jsonEquivalent(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func claimedRun(t *testing.T, s TargetStore) (TargetStore, store.Ticket, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "target contract", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := uuid.NewString()
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	return s, ticket, runID, startedAt
}
