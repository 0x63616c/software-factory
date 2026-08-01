package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/0x63616c/software-factory/internal/blobs"
)

// ArtifactStore persists content-addressed agent artifacts outside Temporal history.
type ArtifactStore struct {
	blobs blobs.Store
}

// NewArtifactStore constructs an artifact store over the shared blob service.
func NewArtifactStore(store blobs.Store) ArtifactStore {
	return ArtifactStore{blobs: store}
}

// StoreText persists immutable final text below a workflow identity.
func (store ArtifactStore) StoreText(ctx context.Context, identity, value string) (TextRef, error) {
	ref, err := store.put(ctx, identity, "text", []byte(value))
	if err != nil {
		return TextRef{}, fmt.Errorf("store agent text: %w", err)
	}
	return TextRef(ref), nil
}

// LoadText verifies and loads immutable final text.
func (store ArtifactStore) LoadText(ctx context.Context, ref TextRef) (string, error) {
	value, err := store.get(ctx, ArtifactRef(ref))
	if err != nil {
		return "", fmt.Errorf("load agent text: %w", err)
	}
	return string(value), nil
}

// StoreResponseSchema persists an immutable structured-output schema.
func (store ArtifactStore) StoreResponseSchema(ctx context.Context, identity string, value []byte) (ResponseSchemaRef, error) {
	ref, err := store.put(ctx, identity, "response-schema", value)
	if err != nil {
		return ResponseSchemaRef{}, fmt.Errorf("store agent response schema: %w", err)
	}
	return ResponseSchemaRef(ref), nil
}

// LoadResponseSchema verifies and loads an immutable structured-output schema.
func (store ArtifactStore) LoadResponseSchema(ctx context.Context, ref ResponseSchemaRef) ([]byte, error) {
	value, err := store.get(ctx, ArtifactRef(ref))
	if err != nil {
		return nil, fmt.Errorf("load agent response schema: %w", err)
	}
	return value, nil
}

// StoreArguments persists immutable provider tool arguments below a workflow identity.
func (store ArtifactStore) StoreArguments(ctx context.Context, identity string, value []byte) (ArgumentsRef, error) {
	ref, err := store.put(ctx, identity, "arguments", value)
	if err != nil {
		return ArgumentsRef{}, fmt.Errorf("store agent tool arguments: %w", err)
	}
	return ArgumentsRef(ref), nil
}

// LoadArguments verifies and loads immutable provider tool arguments.
func (store ArtifactStore) LoadArguments(ctx context.Context, ref ArgumentsRef) ([]byte, error) {
	value, err := store.get(ctx, ArtifactRef(ref))
	if err != nil {
		return nil, fmt.Errorf("load agent tool arguments: %w", err)
	}
	return value, nil
}

// StoreOutput persists immutable oversized tool output below a workflow identity.
func (store ArtifactStore) StoreOutput(ctx context.Context, identity string, value []byte) (OutputRef, error) {
	ref, err := store.put(ctx, identity, "output", value)
	if err != nil {
		return OutputRef{}, fmt.Errorf("store agent tool output: %w", err)
	}
	return OutputRef(ref), nil
}

// LoadOutput verifies and loads immutable oversized tool output.
func (store ArtifactStore) LoadOutput(ctx context.Context, ref OutputRef) ([]byte, error) {
	value, err := store.get(ctx, ArtifactRef(ref))
	if err != nil {
		return nil, fmt.Errorf("load agent tool output: %w", err)
	}
	return value, nil
}

func (store ArtifactStore) put(ctx context.Context, identity, kind string, value []byte) (ArtifactRef, error) {
	digestBytes := sha256.Sum256(value)
	digest := hex.EncodeToString(digestBytes[:])
	key, err := blobs.NewKey(blobs.BucketConversations, fmt.Sprintf("%s/artifacts/%s/%s", identity, kind, digest))
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("name agent %s artifact: %w", kind, err)
	}
	if err := store.blobs.Put(ctx, key, value); err != nil {
		return ArtifactRef{}, fmt.Errorf("store agent %s artifact: %w", kind, err)
	}
	return ArtifactRef{Key: key.String(), Bytes: int64(len(value)), Digest: digest}, nil
}

func (store ArtifactStore) get(ctx context.Context, ref ArtifactRef) ([]byte, error) {
	key, err := blobs.ParseKey(ref.Key)
	if err != nil {
		return nil, fmt.Errorf("parse agent artifact reference: %w", err)
	}
	if key.Bucket != blobs.BucketConversations {
		return nil, fmt.Errorf("agent artifact key %q is in the wrong bucket", key)
	}
	value, err := store.blobs.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("load agent artifact %q: %w", key, err)
	}
	digestBytes := sha256.Sum256(value)
	if hex.EncodeToString(digestBytes[:]) != ref.Digest {
		return nil, fmt.Errorf("load agent artifact %q: digest mismatch", key)
	}
	if int64(len(value)) != ref.Bytes {
		return nil, fmt.Errorf("load agent artifact %q: byte count mismatch", key)
	}
	return value, nil
}
