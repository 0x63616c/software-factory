package blobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type fileStore struct {
	root string
}

// NewFileStore returns a Store rooted at dir, which must already exist.
func NewFileStore(dir string) (Store, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat blob store root %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("blob store root %q is not a directory", dir)
	}

	return &fileStore{root: dir}, nil
}

func (store *fileStore) Put(_ context.Context, key Key, value []byte) (err error) {
	target := store.path(key)
	directory := filepath.Dir(target)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create blob directory %q: %w", directory, err)
	}

	stored, err := os.ReadFile(target)
	switch {
	case err == nil && bytes.Equal(stored, value):
		return nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read existing blob %q: %w", key, err)
	}

	temporary, err := os.CreateTemp(directory, ".blob-*")
	if err != nil {
		return fmt.Errorf("create temporary blob for %q: %w", key, err)
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		if open {
			if closeErr := temporary.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("close temporary blob for %q: %w", key, closeErr)
			}
		}
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("remove temporary blob for %q: %w", key, removeErr)
		}
	}()

	if err := temporary.Chmod(0o640); err != nil {
		return fmt.Errorf("set temporary blob permissions for %q: %w", key, err)
	}
	if _, err := temporary.Write(value); err != nil {
		return fmt.Errorf("write temporary blob for %q: %w", key, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary blob for %q: %w", key, err)
	}
	open = false
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("replace blob %q: %w", key, err)
	}

	return nil
}

func (store *fileStore) Get(_ context.Context, key Key) ([]byte, error) {
	value, err := os.ReadFile(store.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("get blob %q: %w", key, ErrNotFound)
		}

		return nil, fmt.Errorf("read blob %q: %w", key, err)
	}

	if digest, ok := digestName(key); ok {
		actual := fmt.Sprintf("%x", sha256.Sum256(value))
		if actual != digest {
			return nil, fmt.Errorf("get blob %q: digest mismatch", key)
		}
	}

	return value, nil
}

func (store *fileStore) path(key Key) string {
	return filepath.Join(store.root, filepath.FromSlash(key.String()))
}

func digestName(key Key) (string, bool) {
	name := filepath.Base(filepath.FromSlash(key.String()))
	if len(name) != sha256.Size*2 {
		return "", false
	}

	for _, character := range name {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", false
		}
	}

	return name, true
}
