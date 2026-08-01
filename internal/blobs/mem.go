package blobs

import (
	"bytes"
	"context"
	"fmt"
	"sync"
)

type memStore struct {
	mu    sync.RWMutex
	blobs map[Key][]byte
}

// NewMemStore creates an in-memory Store safe for concurrent use.
func NewMemStore() Store {
	return &memStore{blobs: make(map[Key][]byte)}
}

func (store *memStore) Put(_ context.Context, key Key, value []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if stored, found := store.blobs[key]; found && bytes.Equal(stored, value) {
		return nil
	}

	store.blobs[key] = bytes.Clone(value)
	return nil
}

func (store *memStore) Get(_ context.Context, key Key) ([]byte, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	stored, found := store.blobs[key]
	if !found {
		return nil, fmt.Errorf("get blob %q: %w", key, ErrNotFound)
	}

	return bytes.Clone(stored), nil
}
