package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0x63616c/software-factory/internal/blobs"
)

// ConversationStore persists immutable, content-addressed conversation revisions.
type ConversationStore struct {
	blobs blobs.Store
}

// NewConversationStore constructs a conversation store over the shared blob service.
func NewConversationStore(store blobs.Store) ConversationStore {
	return ConversationStore{blobs: store}
}

// Append stores one new revision below identity.
func (store ConversationStore) Append(
	ctx context.Context,
	identity string,
	predecessor *ConversationRef,
	items []ConversationItem,
) (ConversationRef, error) {
	revision := 0
	if predecessor != nil {
		predecessorIdentity, err := conversationIdentity(*predecessor)
		if err != nil {
			return ConversationRef{}, fmt.Errorf("verify predecessor: %w", err)
		}
		if predecessorIdentity != identity {
			return ConversationRef{}, fmt.Errorf(
				"predecessor belongs to %q, not %q",
				predecessorIdentity,
				identity,
			)
		}
		if _, err := store.Load(ctx, *predecessor); err != nil {
			return ConversationRef{}, fmt.Errorf("verify predecessor: %w", err)
		}
		revision = predecessor.Revision + 1
	}
	record := ConversationRevision{Predecessor: predecessor, Items: items}
	encoded, err := json.Marshal(record)
	if err != nil {
		return ConversationRef{}, fmt.Errorf("encode conversation revision %d: %w", revision, err)
	}
	digestBytes := sha256.Sum256(encoded)
	digest := hex.EncodeToString(digestBytes[:])
	key, err := blobs.NewKey(blobs.BucketConversations, fmt.Sprintf("%s/%d/%s", identity, revision, digest))
	if err != nil {
		return ConversationRef{}, fmt.Errorf("name conversation revision %d: %w", revision, err)
	}
	if err := store.blobs.Put(ctx, key, encoded); err != nil {
		return ConversationRef{}, fmt.Errorf("store conversation revision %d: %w", revision, err)
	}
	bytes := int64(len(encoded))
	if predecessor != nil {
		bytes += predecessor.Bytes
	}
	return ConversationRef{Key: key.String(), Revision: revision, Bytes: bytes, Digest: digest}, nil
}

// Load reads and verifies one immutable conversation revision.
func (store ConversationStore) Load(ctx context.Context, ref ConversationRef) (ConversationRevision, error) {
	if _, err := conversationIdentity(ref); err != nil {
		return ConversationRevision{}, fmt.Errorf("load conversation revision %d: %w", ref.Revision, err)
	}
	key, err := blobs.ParseKey(ref.Key)
	if err != nil {
		return ConversationRevision{}, fmt.Errorf("load conversation revision %d: %w", ref.Revision, err)
	}
	encoded, err := store.blobs.Get(ctx, key)
	if err != nil {
		return ConversationRevision{}, fmt.Errorf("load conversation revision %d: %w", ref.Revision, err)
	}
	actualDigest := sha256.Sum256(encoded)
	if hex.EncodeToString(actualDigest[:]) != ref.Digest {
		return ConversationRevision{}, fmt.Errorf("load conversation revision %d: digest mismatch", ref.Revision)
	}
	var revision ConversationRevision
	if err := json.Unmarshal(encoded, &revision); err != nil {
		return ConversationRevision{}, fmt.Errorf("decode conversation revision %d: %w", ref.Revision, err)
	}
	expectedBytes := int64(len(encoded))
	if revision.Predecessor != nil {
		expectedBytes += revision.Predecessor.Bytes
	}
	if expectedBytes != ref.Bytes {
		return ConversationRevision{}, fmt.Errorf("load conversation revision %d: byte count mismatch", ref.Revision)
	}
	return revision, nil
}

// Items reconstructs the provider-neutral conversation in revision order.
func (store ConversationStore) Items(ctx context.Context, ref ConversationRef) ([]ConversationItem, error) {
	revisions := make([]ConversationRevision, ref.Revision+1)
	current := ref
	for revisionIndex := ref.Revision; revisionIndex >= 0; revisionIndex-- {
		if current.Revision != revisionIndex {
			return nil, fmt.Errorf("conversation revision chain jumps from %d to %d", revisionIndex, current.Revision)
		}
		revision, err := store.Load(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("reconstruct conversation at revision %d: %w", revisionIndex, err)
		}
		revisions[revisionIndex] = revision
		if revisionIndex == 0 {
			if revision.Predecessor != nil {
				return nil, fmt.Errorf("conversation revision zero has a predecessor")
			}
			break
		}
		if revision.Predecessor == nil {
			return nil, fmt.Errorf("conversation revision %d has no predecessor", revisionIndex)
		}
		current = *revision.Predecessor
	}
	var items []ConversationItem
	for _, revision := range revisions {
		items = append(items, revision.Items...)
	}
	return items, nil
}

// Identity returns the workflow-owned identity encoded by a conversation reference.
func (store ConversationStore) Identity(ref ConversationRef) (string, error) {
	return conversationIdentity(ref)
}

func conversationIdentity(ref ConversationRef) (string, error) {
	key, err := blobs.ParseKey(ref.Key)
	if err != nil {
		return "", fmt.Errorf("parse conversation reference: %w", err)
	}
	if key.Bucket != blobs.BucketConversations {
		return "", fmt.Errorf("key %q is not a conversation", key)
	}
	suffix := fmt.Sprintf("/%d/%s", ref.Revision, ref.Digest)
	if !strings.HasSuffix(key.Path, suffix) {
		return "", fmt.Errorf("key %q does not match revision reference", key)
	}
	identity := strings.TrimSuffix(key.Path, suffix)
	if identity == "" {
		return "", fmt.Errorf("key %q has no conversation identity", key)
	}
	return identity, nil
}
