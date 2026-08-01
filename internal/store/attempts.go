package store

import (
	"context"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/0x63616c/software-factory/internal/work"
)

// AttemptRecorder records a new Attempt of a Step: which model it ran on,
// what it cost, and whether it was actually measured.
type AttemptRecorder interface {
	RecordAttempt(ctx context.Context, key work.StageKey, attemptNo int, model work.Model, usage work.Usage, measured bool, startedAt time.Time) (Attempt, error)
}

// AttemptEnder records how an Attempt ended.
type AttemptEnder interface {
	EndAttempt(ctx context.Context, key work.StageKey, attemptNo int, endedAt time.Time, result AttemptResult) (Attempt, error)
}

// AttemptReader reads every Attempt of one Step, or of a whole Run.
type AttemptReader interface {
	AttemptsForStep(ctx context.Context, key work.StageKey) ([]Attempt, error)
	AttemptsForRun(ctx context.Context, runID string) ([]Attempt, error)
}

// RecordAttempt records attemptNo of the Step key identifies.
func (s *Store) RecordAttempt(
	ctx context.Context, key work.StageKey, attemptNo int,
	model work.Model, usage work.Usage, measured bool, startedAt time.Time,
) (Attempt, error) {
	runID, err := pgUUID(key.RunID)
	if err != nil {
		return Attempt{}, fmt.Errorf("recording attempt %d of step %s: %w", attemptNo, key, err)
	}
	row, err := s.q.RecordAttempt(ctx, storedb.RecordAttemptParams{
		RunID:             runID,
		Stage:             string(key.Stage),
		Turn:              int32(key.Turn),
		AttemptNo:         int32(attemptNo),
		Model:             model.Name,
		Effort:            model.Effort,
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		OutputTokens:      usage.OutputTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		Measured:          measured,
		StartedAt:         pgTimestamp(startedAt),
	})
	if err != nil {
		return Attempt{}, fmt.Errorf("recording attempt %d of step %s: %w", attemptNo, key, wrapQueryErr(err))
	}
	return attemptFromRow(row, key.Ticket), nil
}

// EndAttempt records that attemptNo of the Step key identifies ended at
// endedAt with result.
func (s *Store) EndAttempt(ctx context.Context, key work.StageKey, attemptNo int, endedAt time.Time, result AttemptResult) (Attempt, error) {
	runID, err := pgUUID(key.RunID)
	if err != nil {
		return Attempt{}, fmt.Errorf("ending attempt %d of step %s: %w", attemptNo, key, err)
	}
	row, err := s.q.EndAttempt(ctx, storedb.EndAttemptParams{
		RunID:     runID,
		Stage:     string(key.Stage),
		Turn:      int32(key.Turn),
		AttemptNo: int32(attemptNo),
		EndedAt:   pgTimestamp(endedAt),
		Result:    pgOptionalText(string(result)),
	})
	if err != nil {
		return Attempt{}, fmt.Errorf("ending attempt %d of step %s: %w", attemptNo, key, wrapQueryErr(err))
	}
	return attemptFromRow(row, key.Ticket), nil
}

// AttemptsForStep lists every Attempt of the Step key identifies, in order.
func (s *Store) AttemptsForStep(ctx context.Context, key work.StageKey) ([]Attempt, error) {
	runID, err := pgUUID(key.RunID)
	if err != nil {
		return nil, fmt.Errorf("listing attempts of step %s: %w", key, err)
	}
	rows, err := s.q.AttemptsForStep(ctx, storedb.AttemptsForStepParams{
		RunID: runID,
		Stage: string(key.Stage),
		Turn:  int32(key.Turn),
	})
	if err != nil {
		return nil, fmt.Errorf("listing attempts of step %s: %w", key, wrapQueryErr(err))
	}
	return attemptsFromRows(rows, key.Ticket), nil
}

// AttemptsForRun lists every Attempt recorded for runID, across every Step, in
// pipeline order.
func (s *Store) AttemptsForRun(ctx context.Context, runID string) ([]Attempt, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return nil, fmt.Errorf("listing attempts for run %s: %w", runID, err)
	}
	rows, err := s.q.AttemptsForRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listing attempts for run %s: %w", runID, wrapQueryErr(err))
	}
	// A run's own ticket is not known to this query; callers that need it
	// (RunDetail) already hold it separately and group these by StepKey.
	return attemptsFromRows(rows, 0), nil
}

// attemptFromRow parses a stored row into an Attempt. ticket is carried
// through from the caller's own key, since the row itself does not have one.
func attemptFromRow(row storedb.Attempt, ticket int) Attempt {
	return Attempt{
		Key: work.StageKey{
			Ticket: ticket,
			RunID:  runIDString(row.RunID),
			Stage:  work.Stage(row.Stage),
			Turn:   int(row.Turn),
		},
		AttemptNo: int(row.AttemptNo),
		Model:     work.Model{Name: row.Model, Effort: row.Effort},
		Usage: work.Usage{
			InputTokens:       row.InputTokens,
			CachedInputTokens: row.CachedInputTokens,
			OutputTokens:      row.OutputTokens,
			ReasoningTokens:   row.ReasoningTokens,
		},
		Measured:  row.Measured,
		StartedAt: timeFromPg(row.StartedAt),
		EndedAt:   timeFromPg(row.EndedAt),
		Result:    AttemptResult(textFromPg(row.Result)),
	}
}

func attemptsFromRows(rows []storedb.Attempt, ticket int) []Attempt {
	attempts := make([]Attempt, 0, len(rows))
	for _, row := range rows {
		attempts = append(attempts, attemptFromRow(row, ticket))
	}
	return attempts
}

var (
	_ AttemptRecorder = (*Store)(nil)
	_ AttemptEnder    = (*Store)(nil)
	_ AttemptReader   = (*Store)(nil)
)
