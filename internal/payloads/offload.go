package payloads

import (
	"context"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/converter"
)

const offloadTimeout = 30 * time.Second

type offloadLayer struct {
	store blobs.Store
}

// newOffloadLayer returns the layer that moves payload bytes into store and leaves a key behind in their place.
func newOffloadLayer(store blobs.Store) Layer {
	return offloadLayer{store: store}
}

func (offloadLayer) Encoding() string {
	return "binary/remote-payload"
}

func (layer offloadLayer) Apply(sc converter.SerializationContext, stored []byte) ([]byte, error) {
	payloadKey := work.NewPayloadKey(sc, stored)
	blobKey, err := blobs.NewKey(blobs.BucketPayloads, payloadKey.String())
	if err != nil {
		return nil, fmt.Errorf("create payload blob key: %w", err)
	}
	encoded := []byte(blobKey.String())
	// layerCodec invokes Apply before it checks whether encoding shrinks the payload.
	// Avoid creating a blob that its pass-through result cannot reference.
	if len(encoded) >= len(stored) {
		return stored, nil
	}

	// Temporal's PayloadCodec.Encode API provides no context. Bound this SDK seam
	// explicitly rather than leaving a workflow task blocked on a store operation.
	ctx, cancel := context.WithTimeout(context.Background(), offloadTimeout)
	defer cancel()
	if err := layer.store.Put(ctx, blobKey, stored); err != nil {
		return nil, fmt.Errorf("put payload blob %q: %w", blobKey, err)
	}

	return encoded, nil
}

func (layer offloadLayer) Unapply(encoded []byte) ([]byte, error) {
	blobKey, err := blobs.ParseKey(string(encoded))
	if err != nil {
		return nil, fmt.Errorf("parse payload blob key: %w", err)
	}

	// Temporal's PayloadCodec.Decode API also provides no context, so reads have
	// the same bounded background context as writes at this SDK seam.
	ctx, cancel := context.WithTimeout(context.Background(), offloadTimeout)
	defer cancel()
	stored, err := layer.store.Get(ctx, blobKey)
	if err != nil {
		return nil, fmt.Errorf("get payload blob %q: %w", blobKey, err)
	}

	return stored, nil
}
