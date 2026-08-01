package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storetest"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTargetStoreConflictContract(t *testing.T) {
	storetest.RunTargetConflictContract(t, func(t *testing.T) storetest.TargetStore {
		return newTestStore(t)
	})
}

// These are the first S3 tracer bullets: every assertion goes through Store,
// against migrated Postgres, rather than reaching into a table.
func TestClaimAndStartRunKeepsOneActiveOwner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "claim", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	firstID := newTestRunID(t)
	first, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: firstID, StartedAt: startedAt})
	if err != nil {
		t.Fatalf("ClaimAndStartRun(first): %v", err)
	}
	if first.Ticket.State != store.TicketActive || first.Ticket.ActiveRunID != firstID {
		t.Fatalf("first claim ticket = %+v, want active owner %s", first.Ticket, firstID)
	}
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: firstID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun(retry): %v", err)
	}
	_, err = s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: newTestRunID(t), StartedAt: startedAt})
	if !errors.Is(err, store.ErrTicketClaimed) {
		t.Fatalf("ClaimAndStartRun(conflict) error = %v, want ErrTicketClaimed", err)
	}
}

func TestClaimAndStartRunSerializesRacingOwners(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "race", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	inputs := []store.ClaimRunInput{{TicketID: ticket.ID, RunID: newTestRunID(t), StartedAt: startedAt}, {TicketID: ticket.ID, RunID: newTestRunID(t), StartedAt: startedAt}}
	start := make(chan struct{})
	errs := make(chan error, len(inputs))
	var group sync.WaitGroup
	for _, in := range inputs {
		group.Add(1)
		go func(in store.ClaimRunInput) {
			defer group.Done()
			<-start
			_, err := s.ClaimAndStartRun(ctx, in)
			errs <- err
		}(in)
	}
	close(start)
	group.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrTicketClaimed):
			conflicts++
		default:
			t.Fatalf("claim error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("claims: %d successes, %d conflicts; want 1 each", successes, conflicts)
	}
}

func TestConfirmedMergeFinalizationIsIrreversible(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	upstream, err := s.CreateTicket(ctx, "upstream", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(upstream): %v", err)
	}
	dependent, err := s.CreateTicket(ctx, "dependent", "", []store.TicketID{upstream.ID})
	if err != nil {
		t.Fatalf("CreateTicket(dependent): %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: upstream.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	result, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: upstream.ID, StepOrdinal: 1, ReviewedHead: "h1", MergeSHA: "m1", EndedAt: startedAt.Add(time.Minute)})
	if err != nil {
		t.Fatalf("FinalizeConfirmedMerge: %v", err)
	}
	if result.Ticket.State != store.TicketDone || result.Run.TargetOutcome != work.RunOutcomeSucceeded || result.Run.MergeSHA != "m1" {
		t.Fatalf("final result = %+v, want done successful m1", result)
	}
	if _, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: upstream.ID, StepOrdinal: 1, ReviewedHead: "h1", MergeSHA: "m1", EndedAt: startedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("FinalizeConfirmedMerge(retry): %v", err)
	}
	if _, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: upstream.ID, StepOrdinal: 1, ReviewedHead: "h1", MergeSHA: "m2", EndedAt: startedAt.Add(time.Minute)}); !errors.Is(err, work.ErrPermanent) {
		t.Fatalf("FinalizeConfirmedMerge(conflict) error = %v, want permanent", err)
	}
	if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: upstream.ID, EndedAt: startedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("CancelRun(after merge): %v", err)
	}
	got, err := s.Ticket(ctx, upstream.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketDone {
		t.Fatalf("Ticket state = %s, want done", got.State)
	}
	ready, err := s.ReadyTickets(ctx)
	if err != nil {
		t.Fatalf("ReadyTickets: %v", err)
	}
	if !containsTicket(ready, dependent.ID) {
		t.Fatalf("ReadyTickets() = %+v, want dependent", ready)
	}
}

func TestFailedRunFinalizationRecordsSemanticDeadlineHistory(t *testing.T) {
	fixture := finalizeSemanticDeadlineRun(t)

	if fixture.result.Ticket.State != store.TicketFailed || fixture.result.Ticket.ActiveRunID != "" || fixture.result.Run.TargetOutcome != work.RunOutcomeFailed || fixture.result.Run.TargetFailure != work.RunFailureSemanticDeadline {
		t.Fatalf("failed result = %+v, want failed ticket/run with semantic deadline", fixture.result)
	}
	detail, err := fixture.store.TargetRunDetail(context.Background(), fixture.runID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Step.State != work.StepStateFailed || len(detail.Steps[0].Attempts) != 1 || detail.Steps[0].Attempts[0].State != work.AgentAttemptFailed || detail.Steps[0].Attempts[0].FailureKind != work.RunFailureSemanticDeadline {
		t.Fatalf("failed history = %+v, want failed Step and running Attempt closed with terminal classification", detail.Steps)
	}
}

func TestFailedRunFinalizationAcceptsAnExactRetry(t *testing.T) {
	fixture := finalizeSemanticDeadlineRun(t)

	if _, err := fixture.store.FinalizeRunFailure(context.Background(), fixture.input); err != nil {
		t.Fatalf("FinalizeRunFailure retry: %v", err)
	}
}

func TestConfirmedMergeCannotReverseAnAlreadyFailedRun(t *testing.T) {
	fixture := finalizeSemanticDeadlineRun(t)

	if _, err := fixture.store.FinalizeConfirmedMerge(context.Background(), store.ConfirmedMergeInput{RunID: fixture.runID, TicketID: fixture.ticket.ID, StepOrdinal: 1, ReviewedHead: "h1", MergeSHA: "m1", EndedAt: fixture.startedAt.Add(2 * time.Minute)}); err == nil {
		t.Fatal("FinalizeConfirmedMerge succeeded after failed terminal outcome")
	}
}

type failedRunFixture struct {
	store     *store.Store
	ticket    store.Ticket
	runID     string
	startedAt time.Time
	input     store.RunFailureInput
	result    store.TerminalResult
}

func finalizeSemanticDeadlineRun(t *testing.T) failedRunFixture {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "deadline", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	runID := newTestRunID(t)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepPlan, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}, AgentStage: work.AgentStagePlan, Model: work.Model{Name: "gpt-5", Effort: "high"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartAgentAttempt: %v", err)
	}
	in := store.RunFailureInput{RunID: runID, TicketID: ticket.ID, Outcome: work.RunOutcomeFailed, FailureKind: work.RunFailureSemanticDeadline, StepOrdinal: 1, StepResult: []byte(`{"kind":"semantic_deadline"}`), EndedAt: startedAt.Add(time.Minute)}
	result, err := s.FinalizeRunFailure(ctx, in)
	if err != nil {
		t.Fatalf("FinalizeRunFailure: %v", err)
	}
	return failedRunFixture{store: s, ticket: ticket, runID: runID, startedAt: startedAt, input: in, result: result}
}

func TestCancellationOnlyReopensItsActiveOwner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "cancel", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	result, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if result.Run.TargetOutcome != work.RunOutcomeCanceled || result.Ticket.State != store.TicketOpen || result.Ticket.ActiveRunID != "" {
		t.Fatalf("CancelRun = %+v, want canceled and reopened", result)
	}
	if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("CancelRun(retry): %v", err)
	}
}

func TestConfirmedMergeReconcilesCancellationThatCommittedFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "cancel then merge", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	result, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "h1", MergeSHA: "m1", EndedAt: startedAt.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("FinalizeConfirmedMerge after cancellation: %v", err)
	}
	if result.Run.TargetOutcome != work.RunOutcomeSucceeded || result.Ticket.State != store.TicketDone {
		t.Fatalf("terminal result = %+v, want confirmed merge to win", result)
	}
}

func TestConfirmedMergeFencesSuccessorThatClaimedReopenedTicket(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "successor claim before confirmed merge", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	firstRunID := newTestRunID(t)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: firstRunID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun(first): %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: firstRunID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep(first merge): %v", err)
	}
	if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: firstRunID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("CancelRun(first): %v", err)
	}

	secondRunID := newTestRunID(t)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: secondRunID, StartedAt: startedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("ClaimAndStartRun(second): %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: secondRunID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("StartStep(second implement): %v", err)
	}
	secondAttemptID := store.TargetAttemptID{RunID: secondRunID, StepOrdinal: 1, AttemptNo: 1}
	if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: secondAttemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "m", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("StartAgentAttempt(second): %v", err)
	}
	if err := s.BindCheckpointCapability(ctx, secondAttemptID, "second-capability"); err != nil {
		t.Fatalf("BindCheckpointCapability(second): %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: secondRunID, Ordinal: 2, Kind: work.StepMergePullRequest, StartedAt: startedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("StartStep(second merge): %v", err)
	}

	confirmedAt := startedAt.Add(3 * time.Minute)
	start := make(chan struct{})
	mergeResults := make(chan store.TerminalResult, 1)
	mergeErrors := make(chan error, 1)
	checkpointErrors := make(chan error, 1)
	go func() {
		<-start
		result, mergeErr := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: firstRunID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "first-head", MergeSHA: "first-merge", EndedAt: confirmedAt})
		mergeResults <- result
		mergeErrors <- mergeErr
	}()
	go func() {
		<-start
		_, checkpointErr := s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: secondAttemptID, Capability: "second-capability", ExecutionID: "second-thread", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, EndedAt: confirmedAt, Result: []byte(`{"kind":"done"}`), Transcript: targetTranscript()})
		checkpointErrors <- checkpointErr
	}()
	close(start)
	result, err := <-mergeResults, <-mergeErrors
	if err != nil {
		t.Fatalf("FinalizeConfirmedMerge(first after successor claim): %v", err)
	}
	if checkpointErr := <-checkpointErrors; checkpointErr != nil && !errors.Is(checkpointErr, store.ErrRunOwnership) {
		t.Fatalf("racing CheckpointAgentAttempt(second) error = %v, want success before fence or ErrRunOwnership after it", checkpointErr)
	}
	if result.Ticket.State != store.TicketDone || result.Ticket.ActiveRunID != "" || result.Run.TargetOutcome != work.RunOutcomeSucceeded || result.Run.MergeSHA != "first-merge" {
		t.Fatalf("confirmed result = %+v, want first Run succeeded and Ticket done", result)
	}
	secondRun, err := s.Run(ctx, secondRunID)
	if err != nil {
		t.Fatalf("Run(second): %v", err)
	}
	if secondRun.TargetOutcome != work.RunOutcomeCanceled {
		t.Fatalf("second Run outcome = %q, want canceled fence", secondRun.TargetOutcome)
	}

	_, err = s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: secondAttemptID, Capability: "second-capability", ExecutionID: "second-thread", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, EndedAt: confirmedAt.Add(time.Minute), Result: []byte(`{"kind":"done"}`), Transcript: targetTranscript()})
	if !errors.Is(err, store.ErrRunOwnership) {
		t.Fatalf("CheckpointAgentAttempt(second after fence) error = %v, want ErrRunOwnership", err)
	}
	_, err = s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: secondRunID, TicketID: ticket.ID, StepOrdinal: 2, ReviewedHead: "second-head", MergeSHA: "second-merge", EndedAt: confirmedAt.Add(time.Minute)})
	if !errors.Is(err, store.ErrRunOwnership) {
		t.Fatalf("FinalizeConfirmedMerge(second after fence) error = %v, want ErrRunOwnership", err)
	}
}

func TestConfirmedMergeRetriesWhenSuccessorClaimsBetweenOwnerObservationAndTicketLock(t *testing.T) {
	s, pool := newTestStoreAndPool(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "successor claims through observed-open gap", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	firstRunID := newTestRunID(t)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: firstRunID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun(first): %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: firstRunID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep(first merge): %v", err)
	}
	if _, err := s.CancelRun(ctx, store.CancelRunInput{RunID: firstRunID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("CancelRun(first): %v", err)
	}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Ticket lock blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	var lockedTicketID int64
	if err := blocker.QueryRow(ctx, "SELECT id FROM ticket WHERE id = $1 FOR UPDATE", ticket.ID).Scan(&lockedTicketID); err != nil {
		t.Fatalf("lock Ticket: %v", err)
	}
	baseline := ticketLockWaiterCount(t, pool)

	secondRunID := newTestRunID(t)
	claimErrors := make(chan error, 1)
	go func() {
		_, claimErr := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: secondRunID, StartedAt: startedAt.Add(2 * time.Minute)})
		claimErrors <- claimErr
	}()
	waitForTicketLockWaiters(t, pool, baseline+1)

	mergeResults := make(chan store.TerminalResult, 1)
	mergeErrors := make(chan error, 1)
	go func() {
		result, mergeErr := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: firstRunID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "first-head", MergeSHA: "first-merge", EndedAt: startedAt.Add(3 * time.Minute)})
		mergeResults <- result
		mergeErrors <- mergeErr
	}()
	waitForTicketLockWaiters(t, pool, baseline+2)

	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release Ticket lock blocker: %v", err)
	}
	select {
	case claimErr := <-claimErrors:
		if claimErr != nil {
			t.Fatalf("ClaimAndStartRun(second): %v", claimErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ClaimAndStartRun(second) deadlocked")
	}
	var result store.TerminalResult
	select {
	case result = <-mergeResults:
		if mergeErr := <-mergeErrors; mergeErr != nil {
			t.Fatalf("FinalizeConfirmedMerge(first): %v", mergeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FinalizeConfirmedMerge(first) deadlocked")
	}
	if result.Ticket.State != store.TicketDone || result.Ticket.ActiveRunID != "" {
		t.Fatalf("confirmed result Ticket = %+v, want done without owner", result.Ticket)
	}
	second, err := s.Run(ctx, secondRunID)
	if err != nil {
		t.Fatalf("Run(second): %v", err)
	}
	if second.TargetOutcome != work.RunOutcomeCanceled {
		t.Fatalf("second Run outcome = %q, want canceled fence", second.TargetOutcome)
	}
}

func ticketLockWaiterCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM pg_stat_activity
		WHERE datname = current_database()
		  AND wait_event_type = 'Lock'
		  AND query LIKE '%FROM ticket WHERE id = $1 FOR UPDATE%'
	`).Scan(&count)
	if err != nil {
		t.Fatalf("count Ticket lock waiters: %v", err)
	}
	return count
}

func waitForTicketLockWaiters(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ticketLockWaiterCount(t, pool) >= want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Ticket lock waiter count did not reach %d", want)
		case <-ticker.C:
		}
	}
}

func TestConfirmedMergeWinsCancellationRace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "cancel merge race", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, err := s.CancelRun(ctx, store.CancelRunInput{RunID: runID, TicketID: ticket.ID, EndedAt: startedAt.Add(time.Minute)})
		errs <- err
	}()
	go func() {
		defer group.Done()
		<-start
		_, err := s.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "head-1", MergeSHA: "merge-1", EndedAt: startedAt.Add(time.Minute)})
		errs <- err
	}()
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("racing terminal operation: %v", err)
		}
	}
	gotTicket, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	gotRun, err := s.Run(ctx, runID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotTicket.State != store.TicketDone || gotRun.TargetOutcome != work.RunOutcomeSucceeded || gotRun.MergeSHA != "merge-1" {
		t.Fatalf("race result = ticket %+v, run %+v; want confirmed merge to win", gotTicket, gotRun)
	}
}

func TestMaintenanceReopensAbandonedOwnershipWithoutClosingTheRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "abandoned", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	reconciled, err := s.ReconcileAbandonedRun(ctx, runID, ticket.ID)
	if err != nil {
		t.Fatalf("ReconcileAbandonedRun: %v", err)
	}
	if !reconciled {
		t.Fatal("ReconcileAbandonedRun = false, want true")
	}
	run, err := s.Run(ctx, runID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.TargetOutcome != "" {
		t.Fatalf("Run target outcome = %q, want no invented terminal result", run.TargetOutcome)
	}
	got, err := s.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if got.State != store.TicketOpen {
		t.Fatalf("Ticket state = %s, want open", got.State)
	}
}

func TestTargetHistoryKeepsInfrastructureAndAgentWorkDistinct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "history", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepCloneRepository, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep(clone): %v", err)
	}
	if _, err := s.CompleteStep(ctx, runID, 1, startedAt.Add(time.Minute), []byte(`{"kind":"cloned"}`)); err != nil {
		t.Fatalf("CompleteStep(clone): %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 2, Kind: work.StepImplement, Iteration: 1, StartedAt: startedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("StartStep(implement): %v", err)
	}
	if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: store.TargetAttemptID{RunID: runID, StepOrdinal: 2, AttemptNo: 1}, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "gpt-5.6-terra", Effort: "medium"}, UsageState: work.UsageMeasured, StartedAt: startedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("StartAgentAttempt(1): %v", err)
	}
	if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: store.TargetAttemptID{RunID: runID, StepOrdinal: 2, AttemptNo: 2}, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "gpt-5.6-terra", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("StartAgentAttempt(2): %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, runID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if len(detail.Steps) != 2 || len(detail.Steps[0].Attempts) != 0 || len(detail.Steps[1].Attempts) != 2 {
		t.Fatalf("TargetRunDetail = %+v, want clone without attempts then two implements", detail)
	}
	if detail.Steps[1].Attempts[0].ID.AttemptNo != 1 || detail.Steps[1].Attempts[1].ID.AttemptNo != 2 {
		t.Fatalf("attempt order = %+v, want 1 then 2", detail.Steps[1].Attempts)
	}
}

func TestTargetHistoryProjectsTranscriptPresence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "target transcript history", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
	if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "m", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartAgentAttempt: %v", err)
	}
	if err := s.BindCheckpointCapability(ctx, attemptID, "capability"); err != nil {
		t.Fatalf("BindCheckpointCapability: %v", err)
	}
	if _, err := s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: attemptID, Capability: "capability", ExecutionID: "thread-1", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, EndedAt: startedAt.Add(time.Minute), Result: []byte(`{"kind":"done"}`), Transcript: targetTranscript()}); err != nil {
		t.Fatalf("CheckpointAgentAttempt: %v", err)
	}
	detail, err := s.TargetRunDetail(ctx, runID)
	if err != nil {
		t.Fatalf("TargetRunDetail: %v", err)
	}
	if len(detail.Steps) != 1 || len(detail.Steps[0].Attempts) != 1 || !detail.Steps[0].Attempts[0].TranscriptPresent {
		t.Fatalf("TargetRunDetail = %+v, want transcript present", detail)
	}
}

func TestCheckpointCapabilityCannotMutateAnotherRunAndGitCheckpointDoesNotRegress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first, err := s.CreateTicket(ctx, "one", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(first): %v", err)
	}
	second, err := s.CreateTicket(ctx, "two", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(second): %v", err)
	}
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	firstRun, secondRun := newTestRunID(t), newTestRunID(t)
	for _, claim := range []store.ClaimRunInput{{TicketID: first.ID, RunID: firstRun, StartedAt: startedAt}, {TicketID: second.ID, RunID: secondRun, StartedAt: startedAt}} {
		if _, err := s.ClaimAndStartRun(ctx, claim); err != nil {
			t.Fatalf("ClaimAndStartRun: %v", err)
		}
	}
	for _, runID := range []string{firstRun, secondRun} {
		if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartStep: %v", err)
		}
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "m", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt: %v", err)
		}
	}
	firstAttempt := store.TargetAttemptID{RunID: firstRun, StepOrdinal: 1, AttemptNo: 1}
	if err := s.BindCheckpointCapability(ctx, firstAttempt, "first-capability"); err != nil {
		t.Fatalf("BindCheckpointCapability: %v", err)
	}
	_, err = s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: store.TargetAttemptID{RunID: secondRun, StepOrdinal: 1, AttemptNo: 1}, Capability: "first-capability", ExecutionID: "thread", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, EndedAt: startedAt.Add(time.Minute), Result: []byte(`{"kind":"done"}`), Transcript: targetTranscript()})
	if !errors.Is(err, store.ErrRunOwnership) {
		t.Fatalf("cross-run checkpoint error = %v, want ErrRunOwnership", err)
	}
	checkpoint := store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{RunID: firstRun, StepOrdinal: 1, Branch: "factory/one", PushedHead: "h1", ObservedBase: "b1", PullRequestNumber: 1, PullRequestNodeID: "node-1", StepResult: []byte(`{"kind":"synced"}`)}, CompletedAt: startedAt.Add(time.Minute)}
	if _, err := s.CheckpointGitEffect(ctx, checkpoint); err != nil {
		t.Fatalf("CheckpointGitEffect: %v", err)
	}
	checkpoint.PushedHead = "old"
	if _, err := s.CheckpointGitEffect(ctx, checkpoint); !errors.Is(err, work.ErrPermanent) {
		t.Fatalf("older git checkpoint error = %v, want permanent", err)
	}
}

func TestSucceededAgentCheckpointRequiresCompleteDurableEvidence(t *testing.T) {
	tests := []struct {
		name       string
		threadID   string
		usageState work.UsageState
		result     []byte
		transcript *store.TargetTranscript
	}{
		{name: "provider identity", usageState: work.UsageMeasured, result: []byte(`{"kind":"done"}`), transcript: targetTranscript()},
		{name: "terminal result", threadID: "thread-1", usageState: work.UsageMeasured, transcript: targetTranscript()},
		{name: "usage state", threadID: "thread-1", result: []byte(`{"kind":"done"}`), transcript: targetTranscript()},
		{name: "transcript", threadID: "thread-1", usageState: work.UsageMeasured, result: []byte(`{"kind":"done"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			ticket, err := s.CreateTicket(ctx, "checkpoint "+tt.name, "", nil)
			if err != nil {
				t.Fatalf("CreateTicket: %v", err)
			}
			runID := newTestRunID(t)
			startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
			if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
				t.Fatalf("ClaimAndStartRun: %v", err)
			}
			if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
				t.Fatalf("StartStep: %v", err)
			}
			attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
			if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "m", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
				t.Fatalf("StartAgentAttempt: %v", err)
			}
			if err := s.BindCheckpointCapability(ctx, attemptID, "capability"); err != nil {
				t.Fatalf("BindCheckpointCapability: %v", err)
			}
			_, err = s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: attemptID, Capability: "capability", ExecutionID: tt.threadID, State: work.AgentAttemptSucceeded, UsageState: tt.usageState, EndedAt: startedAt.Add(time.Minute), Result: tt.result, Transcript: tt.transcript})
			if !errors.Is(err, work.ErrPermanent) {
				t.Fatalf("CheckpointAgentAttempt error = %v, want permanent invalid evidence", err)
			}
		})
	}
}

func TestAgentCheckpointRetryPropagatesTranscriptReadFailure(t *testing.T) {
	for _, state := range []work.AgentAttemptState{work.AgentAttemptRunning, work.AgentAttemptSucceeded} {
		t.Run(string(state), func(t *testing.T) {
			s, pool, checkpoint, startedAt := persistedRunningCheckpoint(t)
			ctx := context.Background()
			if state == work.AgentAttemptSucceeded {
				checkpoint.State = work.AgentAttemptSucceeded
				checkpoint.EndedAt = startedAt.Add(time.Minute)
				checkpoint.Result = []byte(`{"kind":"done"}`)
				checkpoint.Transcript = targetTranscript()
				if _, err := s.CheckpointAgentAttempt(ctx, checkpoint); err != nil {
					t.Fatalf("CheckpointAgentAttempt(terminal): %v", err)
				}
			}

			readFailure := errors.New("transcript persistence unavailable")
			failing := store.New(&transcriptReadFailurePool{Pool: pool, err: readFailure})
			_, err := failing.CheckpointAgentAttempt(ctx, checkpoint)
			if !errors.Is(err, readFailure) {
				t.Fatalf("CheckpointAgentAttempt(retry) error = %v, want transcript read failure", err)
			}
			if errors.Is(err, work.ErrPermanent) {
				t.Fatalf("CheckpointAgentAttempt(retry) error = %v, want retryable persistence failure", err)
			}
		})
	}
}

func persistedRunningCheckpoint(t *testing.T) (*store.Store, *pgxpool.Pool, store.AgentCheckpointInput, time.Time) {
	t.Helper()
	s, pool := newTestStoreAndPool(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "checkpoint read failure", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
	if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "m", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartAgentAttempt: %v", err)
	}
	if err := s.BindCheckpointCapability(ctx, attemptID, "capability"); err != nil {
		t.Fatalf("BindCheckpointCapability: %v", err)
	}
	checkpoint := store.AgentCheckpointInput{
		ID: attemptID, Capability: "capability", ExecutionID: "thread-1",
		State: work.AgentAttemptRunning, UsageState: work.UsageMeasured,
		Transcript: &store.TargetTranscript{CompressedBytes: []byte("partial"), Compression: "zstd", UncompressedSizeBytes: 7, Checksum: []byte("checksum")},
	}
	if _, err := s.CheckpointAgentAttempt(ctx, checkpoint); err != nil {
		t.Fatalf("CheckpointAgentAttempt(running): %v", err)
	}
	return s, pool, checkpoint, startedAt
}

type transcriptReadFailurePool struct {
	*pgxpool.Pool
	err error
}

func (p *transcriptReadFailurePool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return transcriptReadFailureTx{Tx: tx, err: p.err}, nil
}

type transcriptReadFailureTx struct {
	pgx.Tx
	err error
}

func (tx transcriptReadFailureTx) QueryRow(ctx context.Context, query string, args ...interface{}) pgx.Row {
	if strings.Contains(query, "FROM run_agent_transcript") {
		return transcriptReadFailureRow{err: tx.err}
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

type transcriptReadFailureRow struct {
	err error
}

func (row transcriptReadFailureRow) Scan(...interface{}) error {
	return row.err
}

func TestCheckpointCapabilityCannotSelectAnotherAttemptInTheSameRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ticket, err := s.CreateTicket(ctx, "same run attempts", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := newTestRunID(t)
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := s.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := s.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	for attemptNo := 1; attemptNo <= 2; attemptNo++ {
		if _, err := s.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: attemptNo}, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "m", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt(%d): %v", attemptNo, err)
		}
	}
	if err := s.BindCheckpointCapability(ctx, store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}, "attempt-one-capability"); err != nil {
		t.Fatalf("BindCheckpointCapability: %v", err)
	}
	_, err = s.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{ID: store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 2}, Capability: "attempt-one-capability", ExecutionID: "thread-2", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, EndedAt: startedAt.Add(time.Minute), Result: []byte(`{"kind":"done"}`), Transcript: targetTranscript()})
	if !errors.Is(err, store.ErrRunOwnership) {
		t.Fatalf("cross-attempt checkpoint error = %v, want ErrRunOwnership", err)
	}
}

func targetTranscript() *store.TargetTranscript {
	return &store.TargetTranscript{CompressedBytes: []byte("transcript"), Compression: "zstd", UncompressedSizeBytes: 10, Checksum: []byte("checksum")}
}
