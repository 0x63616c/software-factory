package storefake

import (
	"context"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// DispatcherState reads what the dispatcher last wrote about itself.
func (f *Store) DispatcherState(_ context.Context) (store.DispatcherState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dispatcherStateSeen {
		return store.DispatcherState{}, notFoundf("dispatcher state")
	}
	state := f.dispatcherState
	state.InFlight = append([]work.InFlightTicket(nil), f.dispatcherState.InFlight...)
	state.Candidates = append([]int(nil), f.dispatcherState.Candidates...)
	return state, nil
}

// PutDispatcherState overwrites the single dispatcher_state row with state.
func (f *Store) PutDispatcherState(_ context.Context, state store.DispatcherState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	state.InFlight = append([]work.InFlightTicket(nil), state.InFlight...)
	state.Candidates = append([]int(nil), state.Candidates...)
	f.dispatcherState = state
	f.dispatcherStateSeen = true
	return nil
}
