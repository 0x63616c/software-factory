package activities

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// TargetAgentEvidenceFinalizeActivityName preserves the established Temporal
// wire name for persisting one AgentWorkflow outcome. Keeping it explicit
// prevents another Go method alias from silently changing replay history.
const TargetAgentEvidenceFinalizeActivityName = "Finalize"

// TargetAgentEvidenceActivities persists a completed AgentWorkflow's
// reference-backed evidence to the target-run recovery record.
type TargetAgentEvidenceActivities struct {
	recorder    TargetRunRecorder
	transcripts agent.TranscriptStore
}

// TargetAgentEvidenceInput names one already-authorized target Attempt.
type TargetAgentEvidenceInput struct {
	AttemptID     store.TargetAttemptID
	Identity      string
	State         work.AgentAttemptState
	Result        *work.StageOutput
	FailureKind   work.RunFailureKind
	Usage         work.Usage
	UsageMeasured bool
	TranscriptRef agent.TranscriptRef
	EndedAt       time.Time
}

// NewTargetAgentEvidenceActivities constructs the main-control evidence bridge.
func NewTargetAgentEvidenceActivities(recorder TargetRunRecorder, blobStore blobs.Store) (*TargetAgentEvidenceActivities, error) {
	if recorder == nil || blobStore == nil {
		return nil, fmt.Errorf("target agent evidence requires recorder and blob store")
	}
	return &TargetAgentEvidenceActivities{recorder: recorder, transcripts: agent.NewTranscriptStore(blobStore)}, nil
}

// Finalize atomically records terminal result or classified failure, usage,
// and any bounded transcript available before WorkOnTicket advances the Step.
func (activities *TargetAgentEvidenceActivities) Finalize(ctx context.Context, input TargetAgentEvidenceInput) error {
	state := input.State
	if input.AttemptID.RunID == "" || input.Identity == "" ||
		(state != work.AgentAttemptRunning && state != work.AgentAttemptSucceeded && state != work.AgentAttemptFailed) ||
		(state != work.AgentAttemptRunning && input.EndedAt.IsZero()) ||
		(state == work.AgentAttemptSucceeded && (input.Result == nil || input.Result.Value() == nil)) {
		return fail(ctx, "validating target agent evidence", fmt.Errorf("attempt identity, agent identity, state, and required terminal outcome are missing: %w", work.ErrPermanent))
	}
	if state == work.AgentAttemptRunning && (!input.EndedAt.IsZero() || input.FailureKind != "" || input.Result != nil) {
		return fail(ctx, "validating target agent evidence", fmt.Errorf("running target evidence cannot be terminal: %w", work.ErrPermanent))
	}
	if state == work.AgentAttemptSucceeded && input.FailureKind != "" {
		return fail(ctx, "validating target agent evidence", fmt.Errorf("successful target evidence cannot carry a failure kind: %w", work.ErrPermanent))
	}
	if state == work.AgentAttemptFailed && (input.FailureKind == "" || input.Result != nil) {
		return fail(ctx, "validating target agent evidence", fmt.Errorf("failed target evidence requires a failure kind and no result: %w", work.ErrPermanent))
	}
	var transcript *store.TargetTranscript
	if input.TranscriptRef.Key != "" {
		identity, err := activities.transcripts.Identity(input.TranscriptRef)
		if err != nil || identity != input.Identity {
			return fail(ctx, "validating target agent transcript identity", fmt.Errorf("transcript does not belong to target agent execution: %w", work.ErrPermanent))
		}
		rawTranscript, err := activities.transcripts.JSONL(ctx, input.TranscriptRef)
		if err != nil {
			return fail(ctx, "loading target agent transcript", err)
		}
		transcript, err = targetTranscript(rawTranscript)
		if err != nil {
			return fail(ctx, "compressing target agent transcript", err)
		}
	}
	result := json.RawMessage(nil)
	if state == work.AgentAttemptSucceeded {
		var err error
		result, err = json.Marshal(*input.Result)
		if err != nil {
			return fail(ctx, "encoding target agent result", err)
		}
		if transcript == nil {
			return fail(ctx, "validating target agent evidence", fmt.Errorf("successful target agent result requires a transcript: %w", work.ErrPermanent))
		}
	}
	usageState := work.UsageUnknown
	if input.UsageMeasured {
		usageState = work.UsageMeasured
	}
	_, err := activities.recorder.FinalizeAgentWorkflowAttempt(ctx, store.AgentCheckpointInput{
		ID: input.AttemptID, ExecutionID: input.Identity, State: state, FailureKind: input.FailureKind,
		UsageState: usageState, Usage: input.Usage, EndedAt: input.EndedAt,
		Result: result, Transcript: transcript,
	})
	if err != nil {
		return fail(ctx, "recording target agent evidence", err)
	}
	return nil
}

func targetTranscript(raw []byte) (*store.TargetTranscript, error) {
	if len(raw) == 0 || len(raw) > work.MaxTargetTranscriptUncompressedBytes {
		return nil, fmt.Errorf("target transcript is outside the durable bound: %w", work.ErrPermanent)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() > work.MaxTargetTranscriptCompressedBytes {
		return nil, fmt.Errorf("target transcript exceeds compressed bound: %w", work.ErrPermanent)
	}
	checksum := sha256.Sum256(raw)
	return &store.TargetTranscript{CompressedBytes: compressed.Bytes(), Compression: "gzip", UncompressedSizeBytes: int64(len(raw)), Checksum: checksum[:]}, nil
}
