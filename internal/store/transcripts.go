package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/jackc/pgx/v5"
)

// TranscriptWriter stores one Attempt's compressed transcript. PersistTranscript
// is this method's one caller (ADR-0012: the existing durable-write chokepoint,
// pointed at Postgres instead of NFS).
type TranscriptWriter interface {
	PutTranscript(ctx context.Context, t Transcript) error
}

// TranscriptReader reads back one Attempt's transcript, for download through
// the API.
type TranscriptReader interface {
	Transcript(ctx context.Context, key work.StageKey, attemptNo int) (Transcript, error)
	TranscriptKeysForRun(ctx context.Context, runID string) ([]TranscriptKey, error)
}

// TranscriptKey identifies one Attempt that has a transcript, without the
// transcript's own bytes — the console's run detail view uses this to render
// "no transcript" for an Attempt this set does not contain, at the cost of
// one small query rather than one per-Attempt fetch of a compressed blob.
type TranscriptKey struct {
	Stage     work.Stage
	Turn      int
	AttemptNo int
}

// PutTranscript stores t. A transcript belongs to exactly one Attempt, which
// must already have been recorded — the table's foreign key enforces it.
func (s *Store) PutTranscript(ctx context.Context, t Transcript) error {
	runID, err := pgUUID(t.Key.RunID)
	if err != nil {
		return fmt.Errorf("storing transcript for attempt %d of step %s: %w", t.AttemptNo, t.Key, err)
	}
	err = s.q.PutTranscript(ctx, storedb.PutTranscriptParams{
		RunID:                 runID,
		Stage:                 string(t.Key.Stage),
		Turn:                  int32(t.Key.Turn),
		AttemptNo:             int32(t.AttemptNo),
		CompressedBytes:       t.CompressedBytes,
		Compression:           t.Compression,
		UncompressedSizeBytes: t.UncompressedSizeBytes,
		Checksum:              t.Checksum,
	})
	if err != nil {
		return fmt.Errorf("storing transcript for attempt %d of step %s: %w", t.AttemptNo, t.Key, wrapQueryErr(err))
	}
	return nil
}

// Transcript reads back the transcript for attemptNo of the Step key
// identifies.
func (s *Store) Transcript(ctx context.Context, key work.StageKey, attemptNo int) (Transcript, error) {
	runID, err := pgUUID(key.RunID)
	if err != nil {
		return Transcript{}, fmt.Errorf("reading transcript for attempt %d of step %s: %w", attemptNo, key, err)
	}
	row, err := s.q.Transcript(ctx, storedb.TranscriptParams{
		RunID:     runID,
		Stage:     string(key.Stage),
		Turn:      int32(key.Turn),
		AttemptNo: int32(attemptNo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Transcript{}, fmt.Errorf("reading transcript for attempt %d of step %s: %w", attemptNo, key, ErrNotFound)
		}
		return Transcript{}, fmt.Errorf("reading transcript for attempt %d of step %s: %w", attemptNo, key, wrapQueryErr(err))
	}
	return Transcript{
		Key:                   key,
		AttemptNo:             attemptNo,
		CompressedBytes:       row.CompressedBytes,
		Compression:           row.Compression,
		UncompressedSizeBytes: row.UncompressedSizeBytes,
		Checksum:              row.Checksum,
	}, nil
}

// TranscriptKeysForRun lists every Attempt of runID that has a stored
// transcript.
func (s *Store) TranscriptKeysForRun(ctx context.Context, runID string) ([]TranscriptKey, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return nil, fmt.Errorf("listing transcript keys for run %s: %w", runID, err)
	}
	rows, err := s.q.TranscriptKeysForRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listing transcript keys for run %s: %w", runID, wrapQueryErr(err))
	}
	keys := make([]TranscriptKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, TranscriptKey{Stage: work.Stage(row.Stage), Turn: int(row.Turn), AttemptNo: int(row.AttemptNo)})
	}
	return keys, nil
}

var (
	_ TranscriptWriter = (*Store)(nil)
	_ TranscriptReader = (*Store)(nil)
)
