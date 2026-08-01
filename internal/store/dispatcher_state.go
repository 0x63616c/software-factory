package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/0x63616c/software-factory/internal/store/storedb"
)

// DispatcherStateReader reads the single dispatcher_state row.
type DispatcherStateReader interface {
	DispatcherState(ctx context.Context) (DispatcherState, error)
}

// DispatcherStateWriter writes the single dispatcher_state row, once per tick.
type DispatcherStateWriter interface {
	PutDispatcherState(ctx context.Context, state DispatcherState) error
}

// DispatcherState reads what the dispatcher last wrote about itself.
func (s *Store) DispatcherState(ctx context.Context) (DispatcherState, error) {
	row, err := s.q.DispatcherState(ctx)
	if err != nil {
		return DispatcherState{}, fmt.Errorf("reading dispatcher state: %w", wrapQueryErr(err))
	}

	var state DispatcherState
	for _, field := range []struct {
		name string
		raw  []byte
		into any
	}{
		{name: "config", raw: row.Config, into: &state.Config},
		{name: "breaker", raw: row.Breaker, into: &state.Breaker},
		{name: "in_flight", raw: row.InFlight, into: &state.InFlight},
		{name: "candidates", raw: row.Candidates, into: &state.Candidates},
	} {
		if err := json.Unmarshal(field.raw, field.into); err != nil {
			return DispatcherState{}, fmt.Errorf("reading dispatcher state: decoding %s: %w", field.name, err)
		}
	}
	state.ConfigError = row.ConfigError
	state.FreeSlots = int(row.FreeSlots)
	state.WrittenAt = timeFromPg(row.WrittenAt)
	return state, nil
}

// PutDispatcherState overwrites the single dispatcher_state row with state.
// The dispatcher calls this once per tick (#551) — it is the write that
// finally makes "what is it going to work on next" answerable.
func (s *Store) PutDispatcherState(ctx context.Context, state DispatcherState) error {
	config, err := json.Marshal(state.Config)
	if err != nil {
		return fmt.Errorf("writing dispatcher state: encoding config: %w", err)
	}
	breaker, err := json.Marshal(state.Breaker)
	if err != nil {
		return fmt.Errorf("writing dispatcher state: encoding breaker: %w", err)
	}
	// in_flight and candidates each carry a JSONB array CHECK constraint, never
	// JSON null, so a nil slice must still encode as "[]" rather than
	// json.Marshal's default for a nil slice.
	inFlight, err := marshalJSONArray(state.InFlight)
	if err != nil {
		return fmt.Errorf("writing dispatcher state: encoding in_flight: %w", err)
	}
	candidates, err := marshalJSONArray(state.Candidates)
	if err != nil {
		return fmt.Errorf("writing dispatcher state: encoding candidates: %w", err)
	}

	err = s.q.PutDispatcherState(ctx, storedb.PutDispatcherStateParams{
		Config:      config,
		ConfigError: state.ConfigError,
		Breaker:     breaker,
		InFlight:    inFlight,
		Candidates:  candidates,
		FreeSlots:   int32(state.FreeSlots),
		WrittenAt:   pgTimestamp(state.WrittenAt),
	})
	if err != nil {
		return fmt.Errorf("writing dispatcher state: %w", wrapQueryErr(err))
	}
	return nil
}

// marshalJSONArray encodes a slice as a JSON array, "[]" for a nil or empty
// one rather than json.Marshal's "null" — the shape dispatcher_state's array
// CHECK constraints require.
func marshalJSONArray[T any](values []T) ([]byte, error) {
	if len(values) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(values)
}

var (
	_ DispatcherStateReader = (*Store)(nil)
	_ DispatcherStateWriter = (*Store)(nil)
)
