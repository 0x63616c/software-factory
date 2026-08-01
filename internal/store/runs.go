package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/jackc/pgx/v5"
)

// RunStarter starts a new Run for a Ticket. The workflow's opening activity is
// this method's caller.
type RunStarter interface {
	StartRun(ctx context.Context, runID string, ticket TicketID, startedAt time.Time) (Run, error)
}

// RunEnder ends a Run with its outcome and failure kind. The workflow's
// terminal activity is this method's caller.
type RunEnder interface {
	EndRun(ctx context.Context, runID string, endedAt time.Time, outcome work.Outcome, failure work.FailureKind) (Run, error)
}

// RunReader reads one Run, or a Run together with every Step and Attempt
// recorded against it — the console's detail view.
type RunReader interface {
	Run(ctx context.Context, runID string) (Run, error)
	RunDetail(ctx context.Context, runID string) (RunDetail, error)
}

// RunLister lists every Run of a Ticket, most recent first — the console
// ticket detail view's top level.
type RunLister interface {
	RunsForTicket(ctx context.Context, ticket TicketID) ([]Run, error)
}

// StartRun records that ticket's Run runID began at startedAt.
func (s *Store) StartRun(ctx context.Context, runID string, ticket TicketID, startedAt time.Time) (Run, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return Run{}, fmt.Errorf("starting run for ticket %d: %w", ticket, err)
	}
	row, err := s.q.StartRun(ctx, storedb.StartRunParams{
		ID:        id,
		TicketID:  int64(ticket),
		StartedAt: pgTimestamp(startedAt),
	})
	if err != nil {
		return Run{}, fmt.Errorf("starting run %s for ticket %d: %w", runID, ticket, wrapQueryErr(err))
	}
	return runFromRow(row), nil
}

// EndRun records how run runID ended.
func (s *Store) EndRun(ctx context.Context, runID string, endedAt time.Time, outcome work.Outcome, failure work.FailureKind) (Run, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return Run{}, fmt.Errorf("ending run: %w", err)
	}
	row, err := s.q.EndRun(ctx, storedb.EndRunParams{
		ID:          id,
		EndedAt:     pgTimestamp(endedAt),
		Outcome:     pgOptionalText(string(outcome)),
		FailureKind: string(failure),
	})
	if err != nil {
		return Run{}, fmt.Errorf("ending run %s: %w", runID, wrapQueryErr(err))
	}
	return runFromRow(row), nil
}

// Run reads one Run by its Temporal run id.
func (s *Store) Run(ctx context.Context, runID string) (Run, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return Run{}, fmt.Errorf("reading run: %w", err)
	}
	row, err := s.q.Run(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, fmt.Errorf("reading run %s: %w", runID, ErrNotFound)
		}
		return Run{}, fmt.Errorf("reading run %s: %w", runID, wrapQueryErr(err))
	}
	return runFromRow(row), nil
}

// RunsForTicket lists every Run of ticket, most recent first.
func (s *Store) RunsForTicket(ctx context.Context, ticket TicketID) ([]Run, error) {
	rows, err := s.q.RunsForTicket(ctx, int64(ticket))
	if err != nil {
		return nil, fmt.Errorf("listing runs for ticket %d: %w", ticket, wrapQueryErr(err))
	}
	runs := make([]Run, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, runFromRow(row))
	}
	return runs, nil
}

// OpenLegacyRuns lists database Runs still owned by working/review Tickets.
func (s *Store) OpenLegacyRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.q.OpenLegacyRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing open legacy runs: %w", wrapQueryErr(err))
	}
	runs := make([]Run, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, runFromRow(row))
	}
	return runs, nil
}

// RunDetail reads a Run together with every Step it has recorded and every
// Attempt of each Step, for the console's detail view. It composes Run,
// StepsForRun and AttemptsForRun rather than a fourth query, because nothing
// here needs a join the three together cannot answer.
func (s *Store) RunDetail(ctx context.Context, runID string) (RunDetail, error) {
	run, err := s.Run(ctx, runID)
	if err != nil {
		return RunDetail{}, err
	}
	steps, err := s.stepsForRun(ctx, runID)
	if err != nil {
		return RunDetail{}, err
	}
	attempts, err := s.AttemptsForRun(ctx, runID)
	if err != nil {
		return RunDetail{}, err
	}

	byStep := make(map[work.StageKey][]Attempt, len(steps))
	for _, a := range attempts {
		key := stepIdentity(a.Key)
		byStep[key] = append(byStep[key], a)
	}

	details := make([]StepDetail, 0, len(steps))
	for _, step := range steps {
		key := stepIdentity(step)
		details = append(details, StepDetail{
			Stage:    step.Stage,
			Turn:     step.Turn,
			Attempts: byStep[key],
		})
	}
	return RunDetail{Run: run, Steps: details}, nil
}

// stepIdentity strips Ticket from a StageKey, so an Attempt (whose Key carries
// the Ticket for logging) groups under the same map key as the Step it
// belongs to, which never carries one.
func stepIdentity(k work.StageKey) work.StageKey {
	return work.StageKey{RunID: k.RunID, Stage: k.Stage, Turn: k.Turn}
}

func runFromRow(row storedb.Run) Run {
	return Run{
		ID:            runIDString(row.ID),
		TicketID:      TicketID(row.TicketID),
		StartedAt:     timeFromPg(row.StartedAt),
		EndedAt:       timeFromPg(row.EndedAt),
		Outcome:       work.Outcome(textFromPg(row.Outcome)),
		Failure:       work.FailureKind(row.FailureKind),
		TargetOutcome: work.RunOutcome(textFromPg(row.TargetOutcome)),
		TargetFailure: work.RunFailureKind(row.TargetFailureKind),
		ReviewedHead:  textFromPg(row.ReviewedHead),
		MergeSHA:      textFromPg(row.MergeSha),
	}
}

var (
	_ RunStarter = (*Store)(nil)
	_ RunEnder   = (*Store)(nil)
	_ RunReader  = (*Store)(nil)
	_ RunLister  = (*Store)(nil)
)
