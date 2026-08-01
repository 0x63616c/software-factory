package storefake

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// StartRun records that ticket's Run runID began at startedAt.
func (f *Store) StartRun(_ context.Context, runID string, ticket store.TicketID, startedAt time.Time) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := store.Run{ID: runID, TicketID: ticket, StartedAt: startedAt}
	f.runs[runID] = run
	return run, nil
}

// EndRun records how run runID ended.
func (f *Store) EndRun(_ context.Context, runID string, endedAt time.Time, outcome work.Outcome, failure work.FailureKind) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return store.Run{}, fmt.Errorf("run %s: %w", runID, errNotFound)
	}
	run.EndedAt = endedAt
	run.Outcome = outcome
	run.Failure = failure
	f.runs[runID] = run
	return run, nil
}

// Run reads one Run by its Temporal run id.
func (f *Store) Run(_ context.Context, runID string) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return store.Run{}, fmt.Errorf("run %s: %w", runID, errNotFound)
	}
	return run, nil
}

// RunsForTicket lists every Run of ticket, most recent (latest StartedAt)
// first, matching the real store's ORDER BY started_at DESC.
func (f *Store) RunsForTicket(_ context.Context, ticket store.TicketID) ([]store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Run
	for _, run := range f.runs {
		if run.TicketID == ticket {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// RunDetail reads a Run together with every Step and Attempt recorded
// against it.
func (f *Store) RunDetail(ctx context.Context, runID string) (store.RunDetail, error) {
	run, err := f.Run(ctx, runID)
	if err != nil {
		return store.RunDetail{}, err
	}

	f.mu.Lock()
	var keys []stepKey
	for k := range f.steps {
		if k.runID == runID {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return f.steps[keys[i]].Before(f.steps[keys[j]]) })

	details := make([]store.StepDetail, 0, len(keys))
	for _, k := range keys {
		var atts []store.Attempt
		for ak, a := range f.attempts {
			if ak.stepKey == k {
				atts = append(atts, a)
			}
		}
		sort.Slice(atts, func(i, j int) bool { return atts[i].AttemptNo < atts[j].AttemptNo })
		details = append(details, store.StepDetail{Stage: k.stage, Turn: k.turn, Attempts: atts})
	}
	f.mu.Unlock()

	return store.RunDetail{Run: run, Steps: details}, nil
}
