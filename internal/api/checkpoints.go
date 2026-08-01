package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// AgentCheckpointStore is the only persistence authority exposed to the Run Worker route.
type AgentCheckpointStore interface {
	CheckpointAgentAttempt(context.Context, store.AgentCheckpointInput) (store.AgentAttempt, error)
	LoadAgentCheckpoint(context.Context, store.TargetAttemptID, string) (store.AgentAttempt, *store.TargetTranscript, bool, error)
}

type agentCheckpointInput struct {
	RunID       string `path:"runID" format:"uuid" doc:"The owning Run identity."`
	StepOrdinal int    `path:"stepOrdinal" minimum:"1" doc:"The active agent-backed Step ordinal."`
	AttemptNo   int    `path:"attemptNo" minimum:"1" doc:"The workflow-authorized Agent Attempt number."`
	Capability  string `header:"X-Software-Factory-Checkpoint-Capability" doc:"The capability minted for this exact active Agent Attempt."`
	Body        checkpoint.Attempt
}

type agentCheckpointReadInput struct {
	RunID       string `path:"runID" format:"uuid" doc:"The owning Run identity."`
	StepOrdinal int    `path:"stepOrdinal" minimum:"1" doc:"The active agent-backed Step ordinal."`
	AttemptNo   int    `path:"attemptNo" minimum:"1" doc:"The workflow-authorized Agent Attempt number."`
	Capability  string `header:"X-Software-Factory-Checkpoint-Capability" doc:"The capability minted for this exact active Agent Attempt."`
}

type agentCheckpointOutput struct{ Body checkpoint.Attempt }

func checkpointOperation(operation *huma.Operation) {
	operation.Summary = "Checkpoint an active Agent Attempt"
	operation.Description = "Stores an opaque execution identity or terminal evidence using a capability scoped to the exact Run, Step, and Agent Attempt."
	operation.Security = []map[string][]string{{"agentCheckpointCapability": {}}}
}

func readCheckpointOperation(operation *huma.Operation) {
	operation.Summary = "Read an active Agent Attempt checkpoint"
	operation.Description = "Reconciles execution progress using the capability scoped to the exact Run, Step, and Agent Attempt."
	operation.Security = []map[string][]string{{"agentCheckpointCapability": {}}}
}

func (service *Service) loadAgentAttemptCheckpoint(ctx context.Context, input *agentCheckpointReadInput) (*agentCheckpointOutput, error) {
	if strings.TrimSpace(input.Capability) == "" {
		return nil, clientError(http.StatusUnauthorized, "checkpoint_unauthorized", "checkpoint capability is required")
	}
	if service.checkpoints == nil {
		return nil, clientError(http.StatusServiceUnavailable, "checkpoint_unavailable", "checkpoint store is not configured")
	}
	attempt, transcript, found, err := service.checkpoints.LoadAgentCheckpoint(ctx, store.TargetAttemptID{RunID: input.RunID, StepOrdinal: input.StepOrdinal, AttemptNo: input.AttemptNo}, input.Capability)
	if err != nil {
		return nil, checkpointStoreError(err)
	}
	if !found {
		return nil, huma.NewError(http.StatusNoContent, "")
	}
	output := &agentCheckpointOutput{}
	output.Body = checkpoint.Attempt{
		ExecutionID: attempt.ExecutionID, State: attempt.State, FailureKind: attempt.FailureKind,
		UsageState: attempt.UsageState,
		Usage:      checkpoint.Usage{InputTokens: attempt.Usage.InputTokens, CachedInputTokens: attempt.Usage.CachedInputTokens, OutputTokens: attempt.Usage.OutputTokens, ReasoningTokens: attempt.Usage.ReasoningTokens},
		EndedAt:    checkpointEndedAtPointer(attempt.EndedAt), Result: attempt.Result, Transcript: checkpointProtocolTranscript(transcript),
	}
	return output, nil
}

func (service *Service) checkpointAgentAttempt(ctx context.Context, input *agentCheckpointInput) (*struct{}, error) {
	if strings.TrimSpace(input.Capability) == "" {
		return nil, clientError(http.StatusUnauthorized, "checkpoint_unauthorized", "checkpoint capability is required")
	}
	if service.checkpoints == nil {
		return nil, clientError(http.StatusServiceUnavailable, "checkpoint_unavailable", "checkpoint store is not configured")
	}

	checkpointInput := store.AgentCheckpointInput{
		ID: store.TargetAttemptID{
			RunID: input.RunID, StepOrdinal: input.StepOrdinal, AttemptNo: input.AttemptNo,
		},
		Capability:  input.Capability,
		ExecutionID: input.Body.ExecutionID,
		State:       input.Body.State,
		FailureKind: input.Body.FailureKind,
		UsageState:  input.Body.UsageState,
		Usage: work.Usage{
			InputTokens: input.Body.Usage.InputTokens, CachedInputTokens: input.Body.Usage.CachedInputTokens,
			OutputTokens: input.Body.Usage.OutputTokens, ReasoningTokens: input.Body.Usage.ReasoningTokens,
		},
		EndedAt:    checkpointEndedAt(input.Body.EndedAt),
		Result:     input.Body.Result,
		Transcript: checkpointTranscript(input.Body.Transcript),
	}
	if err := checkpointInput.Validate(); err != nil {
		return nil, clientError(http.StatusUnprocessableEntity, "invalid_checkpoint", "checkpoint evidence is invalid")
	}
	if _, err := service.checkpoints.CheckpointAgentAttempt(ctx, checkpointInput); err != nil {
		return nil, checkpointStoreError(err)
	}
	return &struct{}{}, nil
}

func checkpointEndedAt(endedAt *time.Time) time.Time {
	if endedAt == nil {
		return time.Time{}
	}
	return endedAt.UTC()
}

func checkpointEndedAtPointer(endedAt time.Time) *time.Time {
	if endedAt.IsZero() {
		return nil
	}
	endedAt = endedAt.UTC()
	return &endedAt
}

func checkpointProtocolTranscript(transcript *store.TargetTranscript) *checkpoint.Transcript {
	if transcript == nil {
		return nil
	}
	return &checkpoint.Transcript{CompressedBytes: transcript.CompressedBytes, Compression: transcript.Compression, UncompressedSizeBytes: transcript.UncompressedSizeBytes, Checksum: transcript.Checksum}
}

func checkpointTranscript(transcript *checkpoint.Transcript) *store.TargetTranscript {
	if transcript == nil {
		return nil
	}
	return &store.TargetTranscript{
		CompressedBytes: transcript.CompressedBytes, Compression: transcript.Compression,
		UncompressedSizeBytes: transcript.UncompressedSizeBytes, Checksum: transcript.Checksum,
	}
}

func checkpointStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrRunOwnership), errors.Is(err, store.ErrNotFound):
		return clientError(http.StatusUnauthorized, "checkpoint_unauthorized", "checkpoint capability does not authorize this attempt")
	case errors.Is(err, work.ErrPermanent):
		return clientError(http.StatusConflict, "checkpoint_conflict", "checkpoint conflicts with durable attempt state")
	default:
		return clientError(http.StatusServiceUnavailable, "checkpoint_unavailable", "checkpoint persistence is temporarily unavailable")
	}
}
