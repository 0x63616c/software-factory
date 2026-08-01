package agent

import (
	"fmt"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/work"
)

// ConversationSeed names the completed implement attempt whose full
// provider-neutral conversation starts a later implement attempt.
//
// TranscriptRef is deliberately absent: an attempt's transcript is its own
// audit log, so copying it would falsely attribute the source attempt's work
// to the target attempt.
type ConversationSeed struct {
	Source          work.StageKey
	SourceIdentity  string
	ConversationRef ConversationRef
}

// ValidateFor confirms that seed can resume target without sharing an attempt
// identity or crossing a ticket or run boundary. It cannot verify the blob
// reference's encoded identity; ConversationStore does that at the I/O boundary.
func (seed ConversationSeed) ValidateFor(target work.StageKey) error {
	if target.Stage != work.StageImplement || seed.Source.Stage != work.StageImplement {
		return fmt.Errorf("conversation seeds only resume implement attempts")
	}
	if seed.Source.Ticket != target.Ticket || seed.Source.RunID != target.RunID {
		return fmt.Errorf("conversation seed source and target must share a ticket and run")
	}
	if seed.Source.Turn < 1 || target.Turn < 1 || seed.Source.Turn >= target.Turn {
		return fmt.Errorf("conversation seed source must be an earlier implement turn")
	}
	if err := ValidateConversationIdentity(seed.SourceIdentity); err != nil {
		return fmt.Errorf("validate conversation seed identity: %w", err)
	}
	if seed.ConversationRef.Key == "" || seed.ConversationRef.Revision < 0 || seed.ConversationRef.Bytes < 1 || seed.ConversationRef.Digest == "" {
		return fmt.Errorf("conversation seed reference is incomplete")
	}
	return nil
}

// ConversationIdentity returns an explicit target identity, or the legacy
// stage-turn identity when callers have no independently owned Attempt ID.
func ConversationIdentity(identity string, key work.StageKey) (string, error) {
	if identity == "" {
		identity = WorkflowID(key.RunID, string(key.Stage), key.Turn)
	}
	if err := ValidateConversationIdentity(identity); err != nil {
		return "", err
	}
	return identity, nil
}

// ValidateConversationIdentity confirms identity can safely be a conversation
// blob path prefix without exposing its storage representation to callers.
func ValidateConversationIdentity(identity string) error {
	if _, err := blobs.NewKey(blobs.BucketConversations, identity+"/0/seed"); err != nil {
		return fmt.Errorf("invalid conversation identity: %w", err)
	}
	return nil
}
