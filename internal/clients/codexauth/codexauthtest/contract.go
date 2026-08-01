// Package codexauthtest holds the conformance suite every SecretStore
// implementation must pass.
//
// It exists because the properties that make the store safe are properties of
// an implementation, not of the interface: a store that applies keys one at a
// time, or that translates a missing precondition into a blind write, satisfies
// the Go interface exactly and destroys the credential anyway. A test written
// against a fake proves only that the fake is right. This suite is the shape of
// the contract itself, so the fake and the real Kubernetes client are held to
// one standard and a new implementation inherits the standard by running it.
package codexauthtest

import (
	"context"
	"errors"
	"testing"

	"github.com/0x63616c/software-factory/internal/clients/codexauth"
	"github.com/0x63616c/software-factory/internal/work"
)

// NewStore returns a store holding seed and nothing else, for one subtest.
//
// Each case gets its own, because the suite advances versions and a shared
// store would make the cases order-dependent. Registering cleanup on t is the
// implementation's business; a store backed by a real apiserver needs it.
type NewStore func(t *testing.T, seed map[string][]byte) codexauth.SecretStore

// RunSecretStoreContract exercises an implementation against every property
// codexauth relies on. Call it from the implementation's own package.
func RunSecretStoreContract(t *testing.T, newStore NewStore) {
	t.Helper()

	t.Run("refuses a write whose precondition has been superseded", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := newStore(t, map[string][]byte{"auth.json": []byte("seed")})

		_, stale, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if _, err := store.Put(ctx, map[string][]byte{"auth.json": []byte("theirs")}, stale); err != nil {
			t.Fatalf("the first write at a current version: %v", err)
		}

		// The stored refresh token is single-use, so a silently applied stale
		// write is not a lost update but a dead credential.
		if _, err := store.Put(ctx, map[string][]byte{"auth.json": []byte("ours")}, stale); !errors.Is(err, work.ErrVersionConflict) {
			t.Fatalf("a write at a superseded version returned %v, want a version conflict", err)
		}
	})

	t.Run("refuses a write that names no precondition", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := newStore(t, map[string][]byte{"auth.json": []byte("seed")})

		// A version that was never set must not be translated into "overwrite
		// whatever is there", and must not be reported as contention either —
		// a caller cannot fix its own bug by retrying it.
		_, err := store.Put(ctx, map[string][]byte{"auth.json": []byte("blind")}, work.SecretVersion{})
		if !errors.Is(err, work.ErrNoPrecondition) {
			t.Fatalf("a write with no precondition returned %v, want it to refuse with work.ErrNoPrecondition", err)
		}

		values, _, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got := string(values["auth.json"]); got != "seed" {
			t.Errorf("auth.json = %q after a refused write, want the stored value untouched", got)
		}
	})

	t.Run("applies a blind write that was asked for by name", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := newStore(t, map[string][]byte{"auth.json": []byte("seed")})

		// Seeding a credential has nothing to compare against, so an
		// unconditional write has to be possible — just never by accident.
		if _, err := store.Put(ctx, map[string][]byte{"auth.json": []byte("reseeded")}, work.Unconditional()); err != nil {
			t.Fatalf("an unconditional write: %v", err)
		}

		values, _, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got := string(values["auth.json"]); got != "reseeded" {
			t.Errorf("auth.json = %q, want the unconditionally written value", got)
		}
	})

	t.Run("chains a write from the version its predecessor returned", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := newStore(t, nil)

		_, version, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// Re-reading between the lease write and the write it protects would
		// adopt whatever landed in between as the precondition — which is the
		// lease's own linearization point, given away.
		next, err := store.Put(ctx, map[string][]byte{"lease": []byte("held")}, version)
		if err != nil {
			t.Fatalf("taking the lease: %v", err)
		}
		if _, err := store.Put(ctx, map[string][]byte{"auth.json": []byte("rotated")}, next); err != nil {
			t.Fatalf("settling at the version the lease write produced: %v", err)
		}
	})

	t.Run("lands every key of a write together", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := newStore(t, map[string][]byte{"auth.json": []byte("old"), "keep": []byte("kept")})

		_, version, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if _, err := store.Put(ctx, map[string][]byte{
			"auth.json": []byte("rotated"),
			"lease":     []byte("cleared"),
		}, version); err != nil {
			t.Fatalf("Put: %v", err)
		}

		values, _, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		for key, want := range map[string]string{"auth.json": "rotated", "lease": "cleared", "keep": "kept"} {
			if got := string(values[key]); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
	})

	t.Run("applies none of a write it refuses", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := newStore(t, map[string][]byte{"auth.json": []byte("old"), "refresh_state.json": []byte("idle")})

		_, stale, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if _, err := store.Put(ctx, map[string][]byte{"auth.json": []byte("theirs")}, stale); err != nil {
			t.Fatalf("the first write at a current version: %v", err)
		}

		// A store that checks the precondition per key rather than for the
		// write as a whole passes every case above and still lands the lease
		// marker and the credential at two linearization points, which is the
		// bug this seam exists to prevent.
		if _, err := store.Put(ctx, map[string][]byte{
			"auth.json":          []byte("ours"),
			"refresh_state.json": []byte("refreshing"),
		}, stale); !errors.Is(err, work.ErrVersionConflict) {
			t.Fatalf("a multi-key write at a superseded version returned %v, want a version conflict", err)
		}

		values, _, err := store.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		for key, want := range map[string]string{"auth.json": "theirs", "refresh_state.json": "idle"} {
			if got := string(values[key]); got != want {
				t.Errorf("%s = %q after a refused write, want %q — the refused write landed in part", key, got, want)
			}
		}
	})
}
