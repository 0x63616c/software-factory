package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/blobs"
)

type corruptingStore struct {
	blobs.Store
}

func (store corruptingStore) Get(ctx context.Context, key blobs.Key) ([]byte, error) {
	value, err := store.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	corrupted := bytes.Clone(value)
	corrupted[len(corrupted)-1] ^= 1
	return corrupted, nil
}

func TestConversationStoreAppendsAnImmutableRevision(t *testing.T) {
	t.Parallel()

	store := agent.NewConversationStore(blobs.NewMemStore())
	wantItems := []agent.ConversationItem{{Kind: agent.ItemUserText, Text: "Design this."}}

	ref, err := store.Append(t.Context(), "agent/run-7/plan", nil, wantItems)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	const digest = "7eadfb8fa56569fd694db3cb9dd5ff9ec13a96278f2d0553c9faee3b5c39609b"
	wantRef := agent.ConversationRef{
		Key:      "conversations/agent/run-7/plan/0/" + digest,
		Revision: 0,
		Bytes:    73,
		Digest:   digest,
	}
	if ref != wantRef {
		t.Fatalf("Append() ref = %+v, want %+v", ref, wantRef)
	}

	loaded, err := store.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Predecessor != nil || !reflect.DeepEqual(loaded.Items, wantItems) {
		t.Fatalf("Load() revision = %+v", loaded)
	}

	wantItems[0].Text = "mutated by caller"
	loadedAgain, err := store.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if loadedAgain.Items[0].Text != "Design this." {
		t.Fatalf("stored text = %q, want immutable original", loadedAgain.Items[0].Text)
	}
}

func TestConversationStoreRejectsTheWrongPredecessorAndCorruptContent(t *testing.T) {
	t.Parallel()

	t.Run("wrong predecessor", func(t *testing.T) {
		store := agent.NewConversationStore(blobs.NewMemStore())
		other, err := store.Append(t.Context(), "agent/run-other/plan", nil, []agent.ConversationItem{{
			Kind: agent.ItemUserText,
			Text: "Other request.",
		}})
		if err != nil {
			t.Fatalf("seed Append() error = %v", err)
		}

		_, err = store.Append(t.Context(), "agent/run-7/plan", &other, []agent.ConversationItem{{
			Kind: agent.ItemFunctionOutput,
			Text: "not related",
		}})
		if err == nil || !strings.Contains(err.Error(), "predecessor belongs to") {
			t.Fatalf("Append() error = %v, want wrong-predecessor error", err)
		}
	})

	t.Run("corrupt content", func(t *testing.T) {
		memory := blobs.NewMemStore()
		writer := agent.NewConversationStore(memory)
		ref, err := writer.Append(t.Context(), "agent/run-7/plan", nil, []agent.ConversationItem{{
			Kind: agent.ItemUserText,
			Text: "Design this.",
		}})
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}

		reader := agent.NewConversationStore(corruptingStore{Store: memory})
		_, err = reader.Load(t.Context(), ref)
		if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("Load() error = %v, want digest mismatch", err)
		}
	})
}

func TestLargeConversationCrossesTheWorkflowSeamAsASmallReference(t *testing.T) {
	t.Parallel()

	const marker = "known-large-conversation-text-that-must-not-enter-history"
	largeText := strings.Repeat(marker, 20_000)
	store := agent.NewConversationStore(blobs.NewMemStore())
	ref, err := store.Append(t.Context(), "agent/run-7/implement/1", nil, []agent.ConversationItem{{
		Kind: agent.ItemUserText,
		Text: largeText,
	}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if ref.Bytes < 1_000_000 {
		t.Fatalf("stored revision bytes = %d, want a large fixture", ref.Bytes)
	}

	payload, err := json.Marshal(agent.ModelTurnInput{ConversationRef: ref})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if bytes.Contains(payload, []byte(marker)) {
		t.Fatalf("workflow payload contains conversation text: %s", payload)
	}
	if len(payload) >= 512 {
		t.Fatalf("workflow payload bytes = %d, want less than 512", len(payload))
	}
}
