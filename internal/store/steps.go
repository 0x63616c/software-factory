package store

import (
	"context"
	"fmt"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/0x63616c/software-factory/internal/work"
)

// StepRecorder records that a Step happened. Idempotent — recording the same
// (run, stage, turn) twice, which an activity retry can do, is a no-op rather
// than a constraint violation.
type StepRecorder interface {
	RecordStep(ctx context.Context, key work.StageKey) error
}

// RecordStep records key.Stage's key.Turn'th Step within key.RunID. key.Ticket
// is not part of a Step's identity and is not written; see Step's doc comment.
func (s *Store) RecordStep(ctx context.Context, key work.StageKey) error {
	runID, err := pgUUID(key.RunID)
	if err != nil {
		return fmt.Errorf("recording step %s: %w", key, err)
	}
	err = s.q.RecordStep(ctx, storedb.RecordStepParams{
		RunID: runID,
		Stage: string(key.Stage),
		Turn:  int32(key.Turn),
	})
	if err != nil {
		return fmt.Errorf("recording step %s: %w", key, wrapQueryErr(err))
	}
	return nil
}

// stepsForRun lists every Step recorded for runID, in pipeline order. Each
// returned StageKey's Ticket field is zero: a Step's own row carries no
// Ticket, and RunDetail is the only caller, which already holds it on Run.
func (s *Store) stepsForRun(ctx context.Context, runID string) ([]work.StageKey, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return nil, fmt.Errorf("listing steps: %w", err)
	}
	rows, err := s.q.StepsForRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listing steps for run %s: %w", runID, wrapQueryErr(err))
	}
	steps := make([]work.StageKey, 0, len(rows))
	for _, row := range rows {
		steps = append(steps, work.StageKey{
			RunID: runIDString(row.RunID),
			Stage: work.Stage(row.Stage),
			Turn:  int(row.Turn),
		})
	}
	return steps, nil
}

var _ StepRecorder = (*Store)(nil)
