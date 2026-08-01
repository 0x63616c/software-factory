package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/work"
)

// TranscriptEventType identifies one provider-neutral durable agent event.
type TranscriptEventType string

const (
	// EventWorkflowPrepared records that the stage prompt and schemas are durable.
	EventWorkflowPrepared TranscriptEventType = "workflow_prepared"
	// EventModelCompleted records one completed direct provider turn.
	EventModelCompleted TranscriptEventType = "model_completed"
	// EventToolCompleted records one completed tool call.
	EventToolCompleted TranscriptEventType = "tool_completed"
	// EventFinalOutputDecoded records successful structured finalization.
	EventFinalOutputDecoded TranscriptEventType = "final_output_decoded"
)

// TranscriptEvent is bounded routing and accounting metadata, never prompt or tool content.
type TranscriptEvent struct {
	Type           TranscriptEventType `json:"type"`
	ModelTurn      int                 `json:"model_turn,omitempty"`
	ToolName       string              `json:"tool_name,omitempty"`
	CallID         string              `json:"call_id,omitempty"`
	Outcome        string              `json:"outcome,omitempty"`
	Usage          work.Usage          `json:"usage,omitempty"`
	UsageMeasured  bool                `json:"usage_measured,omitempty"`
	IsError        bool                `json:"is_error,omitempty"`
	DurationMillis int64               `json:"duration_millis,omitempty"`
}

type transcriptRevision struct {
	Predecessor *TranscriptRef  `json:"predecessor"`
	Event       TranscriptEvent `json:"event"`
}

// TranscriptStore persists immutable provider-neutral transcript revisions.
type TranscriptStore struct {
	blobs blobs.Store
}

// NewTranscriptStore constructs a transcript store over the shared blob service.
func NewTranscriptStore(store blobs.Store) TranscriptStore {
	return TranscriptStore{blobs: store}
}

// Append stores one immutable transcript event below identity.
func (store TranscriptStore) Append(
	ctx context.Context,
	identity string,
	predecessor *TranscriptRef,
	event TranscriptEvent,
) (TranscriptRef, error) {
	revision := 0
	if predecessor != nil {
		predecessorIdentity, err := transcriptIdentity(*predecessor)
		if err != nil {
			return TranscriptRef{}, fmt.Errorf("verify transcript predecessor: %w", err)
		}
		if predecessorIdentity != identity {
			return TranscriptRef{}, fmt.Errorf("transcript predecessor belongs to %q, not %q", predecessorIdentity, identity)
		}
		if _, err := store.load(ctx, *predecessor); err != nil {
			return TranscriptRef{}, fmt.Errorf("verify transcript predecessor: %w", err)
		}
		revision = predecessor.Revision + 1
	}
	record := transcriptRevision{Predecessor: predecessor, Event: event}
	encoded, err := json.Marshal(record)
	if err != nil {
		return TranscriptRef{}, fmt.Errorf("encode transcript revision %d: %w", revision, err)
	}
	digestBytes := sha256.Sum256(encoded)
	digest := hex.EncodeToString(digestBytes[:])
	key, err := blobs.NewKey(blobs.BucketConversations, fmt.Sprintf("%s/transcript/%d/%s", identity, revision, digest))
	if err != nil {
		return TranscriptRef{}, fmt.Errorf("name transcript revision %d: %w", revision, err)
	}
	if err := store.blobs.Put(ctx, key, encoded); err != nil {
		return TranscriptRef{}, fmt.Errorf("store transcript revision %d: %w", revision, err)
	}
	storedBytes := int64(len(encoded))
	if predecessor != nil {
		storedBytes += predecessor.Bytes
	}
	return TranscriptRef{Key: key.String(), Revision: revision, Bytes: storedBytes, Digest: digest}, nil
}

// Events reconstructs the transcript in event order.
func (store TranscriptStore) Events(ctx context.Context, ref TranscriptRef) ([]TranscriptEvent, error) {
	events := make([]TranscriptEvent, ref.Revision+1)
	current := ref
	for revisionIndex := ref.Revision; revisionIndex >= 0; revisionIndex-- {
		if current.Revision != revisionIndex {
			return nil, fmt.Errorf("transcript revision chain jumps from %d to %d", revisionIndex, current.Revision)
		}
		revision, err := store.load(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("reconstruct transcript at revision %d: %w", revisionIndex, err)
		}
		events[revisionIndex] = revision.Event
		if revisionIndex == 0 {
			if revision.Predecessor != nil {
				return nil, fmt.Errorf("transcript revision zero has a predecessor")
			}
			break
		}
		if revision.Predecessor == nil {
			return nil, fmt.Errorf("transcript revision %d has no predecessor", revisionIndex)
		}
		current = *revision.Predecessor
	}
	return events, nil
}

// JSONL renders the provider-neutral event log for database persistence and display.
func (store TranscriptStore) JSONL(ctx context.Context, ref TranscriptRef) ([]byte, error) {
	events, err := store.Events(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("render transcript JSONL: %w", err)
	}
	var result bytes.Buffer
	encoder := json.NewEncoder(&result)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return nil, fmt.Errorf("encode transcript event: %w", err)
		}
	}
	return result.Bytes(), nil
}

// Identity returns the agent workflow identity encoded in a transcript reference.
func (store TranscriptStore) Identity(ref TranscriptRef) (string, error) {
	return transcriptIdentity(ref)
}

func (store TranscriptStore) load(ctx context.Context, ref TranscriptRef) (transcriptRevision, error) {
	if _, err := transcriptIdentity(ref); err != nil {
		return transcriptRevision{}, fmt.Errorf("validate transcript reference: %w", err)
	}
	key, err := blobs.ParseKey(ref.Key)
	if err != nil {
		return transcriptRevision{}, fmt.Errorf("parse transcript storage key: %w", err)
	}
	encoded, err := store.blobs.Get(ctx, key)
	if err != nil {
		return transcriptRevision{}, fmt.Errorf("load transcript revision %d: %w", ref.Revision, err)
	}
	digestBytes := sha256.Sum256(encoded)
	if hex.EncodeToString(digestBytes[:]) != ref.Digest {
		return transcriptRevision{}, fmt.Errorf("load transcript revision %d: digest mismatch", ref.Revision)
	}
	var revision transcriptRevision
	if err := json.Unmarshal(encoded, &revision); err != nil {
		return transcriptRevision{}, fmt.Errorf("decode transcript revision %d: %w", ref.Revision, err)
	}
	expectedBytes := int64(len(encoded))
	if revision.Predecessor != nil {
		expectedBytes += revision.Predecessor.Bytes
	}
	if expectedBytes != ref.Bytes {
		return transcriptRevision{}, fmt.Errorf("load transcript revision %d: byte count mismatch", ref.Revision)
	}
	return revision, nil
}

func transcriptIdentity(ref TranscriptRef) (string, error) {
	key, err := blobs.ParseKey(ref.Key)
	if err != nil {
		return "", fmt.Errorf("parse transcript reference: %w", err)
	}
	if key.Bucket != blobs.BucketConversations {
		return "", fmt.Errorf("key %q is not an agent transcript", key)
	}
	suffix := fmt.Sprintf("/transcript/%d/%s", ref.Revision, ref.Digest)
	if !strings.HasSuffix(key.Path, suffix) {
		return "", fmt.Errorf("key %q does not match transcript reference", key)
	}
	identity := strings.TrimSuffix(key.Path, suffix)
	if identity == "" {
		return "", fmt.Errorf("key %q has no transcript identity", key)
	}
	return identity, nil
}
