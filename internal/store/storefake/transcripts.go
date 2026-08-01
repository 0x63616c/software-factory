package storefake

import (
	"context"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// PutTranscript stores t.
func (f *Store) PutTranscript(_ context.Context, t store.Transcript) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transcripts[attemptKey{stepKey: stepKeyOf(t.Key), attemptNo: t.AttemptNo}] = t
	return nil
}

// Transcript reads back the transcript for attemptNo of the Step key
// identifies.
func (f *Store) Transcript(_ context.Context, key work.StageKey, attemptNo int) (store.Transcript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.transcripts[attemptKey{stepKey: stepKeyOf(key), attemptNo: attemptNo}]
	if !ok {
		return store.Transcript{}, notFoundf("transcript for attempt %d of step %s", attemptNo, key)
	}
	return t, nil
}

// TranscriptKeysForRun lists every Attempt of runID that has a stored
// transcript.
func (f *Store) TranscriptKeysForRun(_ context.Context, runID string) ([]store.TranscriptKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.TranscriptKey
	for ak := range f.transcripts {
		if ak.runID == runID {
			out = append(out, store.TranscriptKey{Stage: ak.stage, Turn: ak.turn, AttemptNo: ak.attemptNo})
		}
	}
	return out, nil
}
