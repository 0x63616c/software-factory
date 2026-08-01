package storefake_test

import (
	"context"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/store/storetest"
	"github.com/0x63616c/software-factory/internal/work"
)

func TestTargetStoreConflictContract(t *testing.T) {
	t.Parallel()
	storetest.RunTargetConflictContract(t, func(*testing.T) storetest.TargetStore {
		return storefake.New()
	})
}

// A merge is externally irreversible before the Store makes its Step and Run
// terminal. The repository-effect checkpoint preserves the exact GitHub result
// for retry, but FinalizeConfirmedMerge is the only transition that completes
// the merge Step and ticket.
func TestRepositoryMergeEffectWaitsForConfirmedMergeFinalization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	runID := "019fb900-0000-7000-8000-000000000001"

	ticket, err := s.CreateTicket(ctx, "merge effect", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep(merge): %v", err)
	}
	identity, err := work.NewRunWorkerIdentity(runID, 1)
	if err != nil {
		t.Fatalf("NewRunWorkerIdentity: %v", err)
	}
	if err := s.BindRepositoryCapability(ctx, identity, "merge-capability"); err != nil {
		t.Fatalf("BindRepositoryCapability: %v", err)
	}
	checkpoint := store.RepositoryCheckpointInput{
		Identity: identity, Capability: "merge-capability",
		GitCheckpoint: store.GitCheckpoint{
			RunID: runID, StepOrdinal: 1, Branch: "factory/merge-effect", PushedHead: "H1", ObservedBase: "B0",
			PullRequestNumber: 42, PullRequestNodeID: "PR_node", StepResult: []byte(`{"kind":"merged","merge_sha":"M1"}`),
		},
		CompletedAt: startedAt.Add(time.Minute),
	}
	if _, err := s.CheckpointRepositoryEffect(ctx, checkpoint); err != nil {
		t.Fatalf("CheckpointRepositoryEffect: %v", err)
	}
	// A lost response retries the exact durable GitHub result; it cannot turn
	// the effect checkpoint into a completed merge Step.
	if _, err := s.CheckpointRepositoryEffect(ctx, checkpoint); err != nil {
		t.Fatalf("CheckpointRepositoryEffect(retry): %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, runID)
	if err != nil {
		t.Fatalf("TargetRunDetail(after effect): %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Step.State != work.StepStateRunning {
		t.Fatalf("merge effect detail = %+v, want one running merge step", detail)
	}

	result, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{
		RunID: runID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "H1", MergeSHA: "M1", EndedAt: startedAt.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("FinalizeConfirmedMerge: %v", err)
	}
	if result.Ticket.State != store.TicketDone || result.Ticket.ActiveRunID != "" || result.Run.TargetOutcome != work.RunOutcomeSucceeded || result.Run.MergeSHA != "M1" {
		t.Fatalf("FinalizeConfirmedMerge result = %+v, want owned ticket done and run successful at M1", result)
	}
	detail, err = s.TargetRunDetail(ctx, runID)
	if err != nil {
		t.Fatalf("TargetRunDetail(after finalization): %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Step.State != work.StepStateCompleted {
		t.Fatalf("final detail = %+v, want completed merge step", detail)
	}
}

// This test never opens a database — it is the one SoftwareStyle requires:
// every consumer built on top of internal/store must be testable without
// Postgres, and this is the fake proving it can carry a whole ticket's
// lifecycle end to end.
func TestFakeStoreCarriesATicketThroughItsWholeLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()

	blocker, err := s.CreateTicket(ctx, "upstream", "do this first", nil)
	if err != nil {
		t.Fatalf("CreateTicket(blocker): %v", err)
	}
	blocked, err := s.CreateTicket(ctx, "downstream", "needs upstream done", nil)
	if err != nil {
		t.Fatalf("CreateTicket(blocked): %v", err)
	}
	if err := s.AddTicketDependency(ctx, blocker.ID, blocked.ID); err != nil {
		t.Fatalf("AddTicketDependency: %v", err)
	}

	ready, err := s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != blocker.ID {
		t.Fatalf("ReadyTickets() = %+v, want only the unblocked ticket %d", ready, blocker.ID)
	}

	if _, err := s.UpdateTicketState(ctx, blocker.ID, store.TicketDone); err != nil {
		t.Fatalf("UpdateTicketState(blocker, done): %v", err)
	}
	ready, err = s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets after blocker done: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != blocked.ID {
		t.Fatalf("ReadyTickets() after blocker done = %+v, want the downstream ticket %d ready", ready, blocked.ID)
	}

	blockers, err := s.TicketBlockers(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("TicketBlockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0].ID != blocker.ID {
		t.Fatalf("TicketBlockers(blocked) = %+v, want [%d]", blockers, blocker.ID)
	}
	blocks, err := s.TicketBlocks(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("TicketBlocks: %v", err)
	}
	if len(blocks) != 1 || blocks[0].ID != blocked.ID {
		t.Fatalf("TicketBlocks(blocker) = %+v, want [%d]", blocks, blocked.ID)
	}

	runID := "11111111-1111-1111-1111-111111111111"
	startedAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if _, err := s.StartRun(ctx, runID, blocked.ID, startedAt); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	key := work.StageKey{Ticket: int(blocked.ID), RunID: runID, Stage: work.StagePlan, Turn: 1}
	if err := s.RecordStep(ctx, key); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}
	model := work.Model{Name: "gpt-5.6-terra", Effort: "medium"}
	usage := work.Usage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 50, ReasoningTokens: 10}
	if _, err := s.RecordAttempt(ctx, key, 1, model, usage, true, startedAt); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	endedAt := startedAt.Add(5 * time.Minute)
	if _, err := s.EndAttempt(ctx, key, 1, endedAt, store.AttemptSucceeded); err != nil {
		t.Fatalf("EndAttempt: %v", err)
	}

	transcript := store.Transcript{
		Key: key, AttemptNo: 1,
		CompressedBytes: []byte("compressed"), Compression: "zstd",
		UncompressedSizeBytes: 42, Checksum: []byte("checksum"),
	}
	if err := s.PutTranscript(ctx, transcript); err != nil {
		t.Fatalf("PutTranscript: %v", err)
	}
	got, err := s.Transcript(ctx, key, 1)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if string(got.CompressedBytes) != "compressed" || got.Compression != "zstd" {
		t.Fatalf("Transcript() = %+v, want the stored transcript back", got)
	}

	if _, err := s.EndRun(ctx, runID, endedAt, work.OutcomeProposed, work.FailureNone); err != nil {
		t.Fatalf("EndRun: %v", err)
	}

	detail, err := s.RunDetail(ctx, runID)
	if err != nil {
		t.Fatalf("RunDetail: %v", err)
	}
	if detail.Run.Outcome != work.OutcomeProposed {
		t.Fatalf("RunDetail().Run.Outcome = %q, want %q", detail.Run.Outcome, work.OutcomeProposed)
	}
	if len(detail.Steps) != 1 || len(detail.Steps[0].Attempts) != 1 {
		t.Fatalf("RunDetail().Steps = %+v, want exactly one step with one attempt", detail.Steps)
	}
	if detail.Steps[0].Attempts[0].Result != store.AttemptSucceeded {
		t.Fatalf("RunDetail() attempt result = %q, want %q", detail.Steps[0].Attempts[0].Result, store.AttemptSucceeded)
	}

	config := work.DefaultConfig()
	config.MaxInFlight = 2
	state := store.DispatcherState{
		Config:     config,
		InFlight:   []work.InFlightTicket{{Ticket: 551, RunID: runID, StartedAt: startedAt}},
		Candidates: []int{552},
		WrittenAt:  endedAt,
	}
	if err := s.PutDispatcherState(ctx, state); err != nil {
		t.Fatalf("PutDispatcherState: %v", err)
	}
	readState, err := s.DispatcherState(ctx)
	if err != nil {
		t.Fatalf("DispatcherState: %v", err)
	}
	if len(readState.InFlight) != 1 || readState.InFlight[0].Ticket != 551 {
		t.Fatalf("DispatcherState().InFlight = %+v, want the one in-flight ticket back", readState.InFlight)
	}
	if len(readState.Candidates) != 1 || readState.Candidates[0] != 552 {
		t.Fatalf("DispatcherState().Candidates = %v, want [552]", readState.Candidates)
	}
}

func TestFakeStoreCreatesTicketWithDeclaredBlockersAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()

	upstream, err := s.CreateTicket(ctx, "upstream", "finish first", nil)
	if err != nil {
		t.Fatalf("CreateTicket(upstream): %v", err)
	}
	downstream, err := s.CreateTicket(ctx, "downstream", "wait", []store.TicketID{upstream.ID})
	if err != nil {
		t.Fatalf("CreateTicket(downstream): %v", err)
	}

	ready, err := s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != upstream.ID {
		t.Fatalf("ReadyTickets() = %+v, want only upstream", ready)
	}
	if _, err := s.CreateTicket(ctx, "invalid", "missing blocker", []store.TicketID{999}); err == nil {
		t.Fatal("CreateTicket with missing blocker succeeded")
	}
	tickets, err := s.Tickets(ctx)
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if len(tickets) != 2 {
		t.Fatalf("Tickets() = %+v, want only the two committed tickets", tickets)
	}

	blockers, err := s.TicketBlockers(ctx, downstream.ID)
	if err != nil {
		t.Fatalf("TicketBlockers(downstream): %v", err)
	}
	if len(blockers) != 1 || blockers[0].ID != upstream.ID {
		t.Fatalf("TicketBlockers(downstream) = %+v, want [%d]", blockers, upstream.ID)
	}
}

// A resumed attempt reports Measured false with a zero Usage, and that must
// stay distinguishable from a real zero-token measurement (#426) — the reason
// Measured exists on Attempt at all.
func TestFakeStoreDistinguishesUnmeasuredFromZero(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()

	ticket, err := s.CreateTicket(ctx, "t", "b", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := "22222222-2222-2222-2222-222222222222"
	startedAt := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	if _, err := s.StartRun(ctx, runID, ticket.ID, startedAt); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	key := work.StageKey{Ticket: int(ticket.ID), RunID: runID, Stage: work.StageImplement, Turn: 1}
	if err := s.RecordStep(ctx, key); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}

	resumed, err := s.RecordAttempt(ctx, key, 1, work.Model{Name: "m", Effort: "low"}, work.Usage{}, false, startedAt)
	if err != nil {
		t.Fatalf("RecordAttempt(resumed): %v", err)
	}
	if resumed.Measured {
		t.Fatal("resumed attempt reports Measured = true, want false")
	}
	if resumed.Usage != (work.Usage{}) {
		t.Fatalf("resumed attempt Usage = %+v, want zero", resumed.Usage)
	}

	measured, err := s.RecordAttempt(ctx, key, 2, work.Model{Name: "m", Effort: "low"}, work.Usage{}, true, startedAt)
	if err != nil {
		t.Fatalf("RecordAttempt(measured): %v", err)
	}
	if !measured.Measured {
		t.Fatal("measured attempt reports Measured = false, want true")
	}
}

func TestFakeStoreDerivesReadinessFromEveryDirectBlocker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()
	done, err := s.CreateTicket(ctx, "done", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(done): %v", err)
	}
	open, err := s.CreateTicket(ctx, "open", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(open): %v", err)
	}
	failed, err := s.CreateTicket(ctx, "failed", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(failed): %v", err)
	}
	mixed, err := s.CreateTicket(ctx, "mixed", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(mixed): %v", err)
	}
	onlyDone, err := s.CreateTicket(ctx, "only done", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(onlyDone): %v", err)
	}
	onlyFailed, err := s.CreateTicket(ctx, "only failed", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(onlyFailed): %v", err)
	}
	if _, err := s.UpdateTicketState(ctx, done.ID, store.TicketDone); err != nil {
		t.Fatalf("UpdateTicketState(done): %v", err)
	}
	if _, err := s.UpdateTicketState(ctx, failed.ID, store.TicketFailed); err != nil {
		t.Fatalf("UpdateTicketState(failed): %v", err)
	}
	for _, edge := range [][2]store.TicketID{{done.ID, mixed.ID}, {open.ID, mixed.ID}, {done.ID, onlyDone.ID}, {failed.ID, onlyFailed.ID}} {
		if err := s.AddTicketDependency(ctx, edge[0], edge[1]); err != nil {
			t.Fatalf("AddTicketDependency(%d, %d): %v", edge[0], edge[1], err)
		}
	}
	ready, err := s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets: %v", err)
	}
	if containsID(ready, mixed.ID) || containsID(ready, onlyFailed.ID) || !containsID(ready, onlyDone.ID) {
		t.Fatalf("ReadyTickets() = %+v, want only-done ready and mixed/failed blocked", ready)
	}
}

// TestFakeStoreRecordWebhookDeliveryAndTransitionAppliesOnceAndUnblocksDownstream
// keeps the fake honest against the real store's transactional behaviour
// (store.TestRecordWebhookDeliveryAndTransitionAppliesOnceAndUnblocksDownstream):
// a delivery applies its transition exactly once, and a redelivery of the
// same id is a no-op.
func TestFakeStoreRecordWebhookDeliveryAndTransitionAppliesOnceAndUnblocksDownstream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storefake.New()

	upstream, err := s.CreateTicket(ctx, "upstream", "merged first", nil)
	if err != nil {
		t.Fatalf("CreateTicket(upstream): %v", err)
	}
	downstream, err := s.CreateTicket(ctx, "downstream", "needs upstream done", nil)
	if err != nil {
		t.Fatalf("CreateTicket(downstream): %v", err)
	}
	if err := s.AddTicketDependency(ctx, upstream.ID, downstream.ID); err != nil {
		t.Fatalf("AddTicketDependency: %v", err)
	}
	if _, err := s.UpdateTicketState(ctx, upstream.ID, store.TicketReview); err != nil {
		t.Fatalf("UpdateTicketState(upstream, review): %v", err)
	}

	outcome, err := s.RecordWebhookDeliveryAndTransition(ctx, "delivery-1", upstream.ID, store.TicketReview, store.TicketDone)
	if err != nil {
		t.Fatalf("RecordWebhookDeliveryAndTransition: %v", err)
	}
	if outcome != store.WebhookDeliveryApplied {
		t.Fatalf("outcome = %v, want WebhookDeliveryApplied", outcome)
	}
	ready, err := s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets: %v", err)
	}
	if !containsID(ready, downstream.ID) {
		t.Fatalf("ReadyTickets() = %+v, want downstream ready as a consequence of upstream done", ready)
	}

	outcome, err = s.RecordWebhookDeliveryAndTransition(ctx, "delivery-1", upstream.ID, store.TicketReview, store.TicketDone)
	if err != nil {
		t.Fatalf("RecordWebhookDeliveryAndTransition (redelivery): %v", err)
	}
	if outcome != store.WebhookDeliveryDuplicate {
		t.Fatalf("redelivery outcome = %v, want WebhookDeliveryDuplicate", outcome)
	}
}

func containsID(tickets []store.Ticket, id store.TicketID) bool {
	for _, ticket := range tickets {
		if ticket.ID == id {
			return true
		}
	}
	return false
}
