package blobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

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

func TestFileStoreGetAbsentIsNotFound(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	_, err = store.Get(t.Context(), newTestKey(t, "workflow-1/run-1/missing"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v, want error wrapping ErrNotFound", err)
	}
}

func TestFileStoreDetectsCorruption(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	want := []byte("payload")
	digest := sha256.Sum256(want)
	key := newTestKey(t, "workflow-1/run-1/"+hex.EncodeToString(digest[:]))
	if err := store.Put(t.Context(), key, want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	path := filepath.Join(root, filepath.FromSlash(key.String()))
	if err := os.WriteFile(path, []byte("corrupted"), 0o640); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = store.Get(t.Context(), key)
	if err == nil {
		t.Fatal("Get() error = nil, want digest mismatch")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("Get() error = %v, want digest mismatch", err)
	}
}

func TestFileStoreSkipsVerificationForNonDigestKeys(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	key := newTestKey(t, "reports/report.txt")
	want := []byte("not content-addressed")
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

func TestFileStorePutIsIdempotent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

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

func TestNewFileStoreRejectsMissingRoot(t *testing.T) {
	_, err := NewFileStore(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("NewFileStore() error = nil, want missing-root error")
	}
}

func TestFileStoreCreatesNestedDirectories(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	key := newTestKey(t, "workflow-1/run-1/nested/payload-1")
	want := []byte("payload")
	if err := store.Put(t.Context(), key, want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	path := filepath.Join(root, filepath.FromSlash(key.String()))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ReadFile() = %q, want %q", got, want)
	}
}
