package blobs

import (
	"context"
	"errors"
)

// ErrNotFound reports a key with no blob behind it.
var ErrNotFound = errors.New("blob not found")

// Store holds opaque bytes under a Key.
//
// Blobs are content-addressed by their callers, so Put is idempotent: writing
// the same bytes to the same key twice is a no-op, not a conflict. There is
// deliberately no Delete — retention is not designed yet, and adding a
// destructive operation before it is would be designing it badly.
type Store interface {
	Put(ctx context.Context, key Key, bytes []byte) error
	Get(ctx context.Context, key Key) ([]byte, error)
}
