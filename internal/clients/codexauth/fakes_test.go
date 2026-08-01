package codexauth

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/0x63616c/software-factory/internal/clients/codexauth/storefake"
	"github.com/0x63616c/software-factory/internal/work"
)

// storedState reads the lease out of a fake store.
func storedState(t *testing.T, store *storefake.Store) refreshState {
	t.Helper()
	state, err := parseRefreshState(store.Read(StateKey))
	if err != nil {
		t.Fatalf("reading the stored lease state: %v", err)
	}
	return state
}

// fakeRefresher is a scripted TokenRefresher.
//
// storeState is snapshotted at the moment of the call rather than after it,
// which is how a test observes that the lease was written before the token was
// presented — the ordering the whole design turns on.
type fakeRefresher struct {
	mu      sync.Mutex
	calls   int
	replies []reply

	// gate blocks inside Refresh, so a test can hold a presentation open while
	// another actor runs.
	gate func(n int)
	// storeState observes the store at the moment of presentation.
	storeState func(n int)
}

type reply struct {
	res     Refreshed
	outcome RefreshOutcome
	err     error
}

func (r *fakeRefresher) Refresh(ctx context.Context, _ work.Credential) (Refreshed, RefreshOutcome, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	gate, observe := r.gate, r.storeState
	var scripted reply
	switch {
	case len(r.replies) == 0:
		scripted = reply{outcome: RefreshUnknown, err: errors.New("the fake refresher was not scripted for this call")}
	case n <= len(r.replies):
		scripted = r.replies[n-1]
	default:
		scripted = r.replies[len(r.replies)-1]
	}
	r.mu.Unlock()

	if observe != nil {
		observe(n)
	}
	if gate != nil {
		gate(n)
	}
	if err := ctx.Err(); err != nil {
		return Refreshed{}, RefreshUnknown, err
	}
	return scripted.res, scripted.outcome, scripted.err
}

func (r *fakeRefresher) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *fakeRefresher) script(replies ...reply) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = replies
}

// recordingMetrics counts what a Source reported, so the operator-visible
// signals are asserted rather than assumed.
type recordingMetrics struct {
	mu        sync.Mutex
	outcomes  []RefreshOutcome
	takeovers int
	deaths    []DeathReason
}

func (m *recordingMetrics) RefreshOutcome(outcome RefreshOutcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes = append(m.outcomes, outcome)
}

func (m *recordingMetrics) Takeover() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.takeovers++
}

func (m *recordingMetrics) CredentialDead(reason DeathReason) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deaths = append(m.deaths, reason)
}

func (m *recordingMetrics) snapshot() (outcomes []RefreshOutcome, takeovers int, deaths []DeathReason) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RefreshOutcome(nil), m.outcomes...), m.takeovers, append([]DeathReason(nil), m.deaths...)
}
