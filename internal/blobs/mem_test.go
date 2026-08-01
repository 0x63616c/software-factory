package blobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestMemStoreRoundTrip(t *testing.T) {
	store := NewMemStore()
	key := newTestKey(t, "workflow-1/run-1/payload-1")
	want := []byte("payload")

	if err := store.Put(t.Context(), key, want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get() = %q, want %q", got, want)
	}
}

func TestMemStoreGetAbsentIsNotFound(t *testing.T) {
	store := NewMemStore()

	_, err := store.Get(t.Context(), newTestKey(t, "workflow-1/run-1/missing"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v, want error wrapping ErrNotFound", err)
	}
}

func TestMemStorePutIsIdempotent(t *testing.T) {
	store := NewMemStore()
	key := newTestKey(t, "workflow-1/run-1/payload-1")
	want := []byte("payload")

	if err := store.Put(t.Context(), key, want); err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	if err := store.Put(t.Context(), key, want); err != nil {
		t.Fatalf("second Put() error = %v", err)
	}

	got, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get() = %q, want %q", got, want)
	}
}

func TestMemStoreGetReturnsACopy(t *testing.T) {
	store := NewMemStore()
	key := newTestKey(t, "workflow-1/run-1/payload-1")
	want := []byte("payload")

	if err := store.Put(t.Context(), key, want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("first Get() error = %v", err)
	}
	got[0] = 'P'

	got, err = store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("second Get() = %q, want %q", got, want)
	}
}

func TestMemStoreIsConcurrencySafe(t *testing.T) {
	store := NewMemStore()
	const workerCount = 32

	keys := make([]Key, workerCount)
	values := make([][]byte, workerCount)
	for worker := range workerCount {
		keys[worker] = newTestKey(t, fmt.Sprintf("workflow-%d/run-1/payload-1", worker))
		values[worker] = []byte(fmt.Sprintf("payload-%d", worker))
	}

	errorsByWorker := make(chan error, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := range workerCount {
		go func() {
			defer workers.Done()

			key := keys[worker]
			want := values[worker]
			if err := store.Put(context.Background(), key, want); err != nil {
				errorsByWorker <- fmt.Errorf("Put(%q): %w", key, err)
				return
			}

			got, err := store.Get(context.Background(), key)
			if err != nil {
				errorsByWorker <- fmt.Errorf("Get(%q): %w", key, err)
				return
			}
			if !bytes.Equal(got, want) {
				errorsByWorker <- fmt.Errorf("Get(%q) = %q, want %q", key, got, want)
			}
		}()
	}
	workers.Wait()
	close(errorsByWorker)

	for err := range errorsByWorker {
		t.Error(err)
	}
}

func newTestKey(t *testing.T, path string) Key {
	t.Helper()

	key, err := NewKey(BucketPayloads, path)
	if err != nil {
		t.Fatalf("NewKey(%q) error = %v", path, err)
	}

	return key
}
