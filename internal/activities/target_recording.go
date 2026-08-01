package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
)

// TargetRunRecorder is the complete mandatory persistence door for the target
// WorkOnTicket workflow. Recording failure is returned to Temporal for retry;
// no target boundary is logged and ignored.
type TargetRunRecorder interface {
	ClaimAndStartRun(context.Context, store.ClaimRunInput) (store.ClaimRunResult, error)
	StartStep(context.Context, store.StartStepInput) (store.RunStep, error)
	CompleteStep(context.Context, string, int, time.Time, json.RawMessage) (store.RunStep, error)
	StartAgentAttempt(context.Context, store.StartAgentAttemptInput) (store.AgentAttempt, error)
	FailAgentAttempt(context.Context, store.AgentAttemptFailureInput) (store.AgentAttempt, error)
	CheckpointAgentAttempt(context.Context, store.AgentCheckpointInput) (store.AgentAttempt, error)
	FinalizeAgentWorkflowAttempt(context.Context, store.AgentCheckpointInput) (store.AgentAttempt, error)
	CheckpointGitEffect(context.Context, store.GitCheckpointInput) (store.GitCheckpoint, error)
	FinalizeConfirmedMerge(context.Context, store.ConfirmedMergeInput) (store.TerminalResult, error)
	CancelRun(context.Context, store.CancelRunInput) (store.TerminalResult, error)
	FinalizeRunFailure(context.Context, store.RunFailureInput) (store.TerminalResult, error)
}

// TargetRecordingActivities adapts target Store persistence to Temporal activities.
type TargetRecordingActivities struct{ store TargetRunRecorder }

// NewTargetRecordingActivities builds the mandatory target recording activity set.
func NewTargetRecordingActivities(recorder TargetRunRecorder) (*TargetRecordingActivities, error) {
	if recorder == nil {
		return nil, fmt.Errorf("target recording activities: a TargetRunRecorder is required")
	}
	return &TargetRecordingActivities{store: recorder}, nil
}

// ClaimAndStartRun persists target ownership before provisioning can begin.
func (a *TargetRecordingActivities) ClaimAndStartRun(ctx context.Context, in store.ClaimRunInput) (store.ClaimRunResult, error) {
	result, err := a.store.ClaimAndStartRun(ctx, in)
	if err != nil {
		return store.ClaimRunResult{}, fail(ctx, fmt.Sprintf("claiming ticket %d", in.TicketID), err)
	}
	return result, nil
}

// StartStep persists a target Step before its primary operation starts.
func (a *TargetRecordingActivities) StartStep(ctx context.Context, in store.StartStepInput) (store.RunStep, error) {
	step, err := a.store.StartStep(ctx, in)
	if err != nil {
		return store.RunStep{}, fail(ctx, fmt.Sprintf("starting step %d of run %s", in.Ordinal, in.RunID), err)
	}
	return step, nil
}

// CompleteStep durably closes a target Step before its caller moves on.
func (a *TargetRecordingActivities) CompleteStep(ctx context.Context, runID string, ordinal int, endedAt time.Time, result json.RawMessage) (store.RunStep, error) {
	step, err := a.store.CompleteStep(ctx, runID, ordinal, endedAt, result)
	if err != nil {
		return store.RunStep{}, fail(ctx, fmt.Sprintf("completing step %d of run %s", ordinal, runID), err)
	}
	return step, nil
}

// StartAgentAttempt persists authorization before an agent can produce a transcript.
func (a *TargetRecordingActivities) StartAgentAttempt(ctx context.Context, in store.StartAgentAttemptInput) (store.AgentAttempt, error) {
	attempt, err := a.store.StartAgentAttempt(ctx, in)
	if err != nil {
		return store.AgentAttempt{}, fail(ctx, fmt.Sprintf("starting agent attempt %s", in.ID), err)
	}
	return attempt, nil
}

// FailAgentAttempt closes an exhausted Agent Attempt before the workflow can authorize a replacement.
func (a *TargetRecordingActivities) FailAgentAttempt(ctx context.Context, in store.AgentAttemptFailureInput) (store.AgentAttempt, error) {
	attempt, err := a.store.FailAgentAttempt(ctx, in)
	if err != nil {
		return store.AgentAttempt{}, fail(ctx, fmt.Sprintf("failing agent attempt %s", in.ID), err)
	}
	return attempt, nil
}

// CheckpointAgentAttempt records terminal agent data before acknowledgement.
func (a *TargetRecordingActivities) CheckpointAgentAttempt(ctx context.Context, in store.AgentCheckpointInput) (store.AgentAttempt, error) {
	attempt, err := a.store.CheckpointAgentAttempt(ctx, in)
	if err != nil {
		return store.AgentAttempt{}, fail(ctx, fmt.Sprintf("checkpointing agent attempt %s", in.ID), err)
	}
	return attempt, nil
}

// CheckpointGitEffect records repository recovery state and closes its Step atomically.
func (a *TargetRecordingActivities) CheckpointGitEffect(ctx context.Context, in store.GitCheckpointInput) (store.GitCheckpoint, error) {
	checkpoint, err := a.store.CheckpointGitEffect(ctx, in)
	if err != nil {
		return store.GitCheckpoint{}, fail(ctx, fmt.Sprintf("checkpointing git effect for run %s step %d", in.RunID, in.StepOrdinal), err)
	}
	return checkpoint, nil
}

// FinalizeConfirmedMerge commits the irreversible terminal outcome.
func (a *TargetRecordingActivities) FinalizeConfirmedMerge(ctx context.Context, in store.ConfirmedMergeInput) (store.TerminalResult, error) {
	result, err := a.store.FinalizeConfirmedMerge(ctx, in)
	if err != nil {
		return store.TerminalResult{}, fail(ctx, fmt.Sprintf("finalizing confirmed merge for run %s", in.RunID), err)
	}
	return result, nil
}

// CancelRun conditionally records cancellation without reopening later owners.
func (a *TargetRecordingActivities) CancelRun(ctx context.Context, in store.CancelRunInput) (store.TerminalResult, error) {
	result, err := a.store.CancelRun(ctx, in)
	if err != nil {
		return store.TerminalResult{}, fail(ctx, fmt.Sprintf("canceling run %s", in.RunID), err)
	}
	return result, nil
}

// CancelRunIfClaimed reconciles cancellation across the claim-response race.
// A missing or later-owned Run means the claim did not become this workflow's
// cancellable ownership and is therefore an idempotent no-op.
func (a *TargetRecordingActivities) CancelRunIfClaimed(ctx context.Context, in store.CancelRunInput) error {
	if _, err := a.store.CancelRun(ctx, in); err != nil && !errors.Is(err, store.ErrRunOwnership) && !errors.Is(err, store.ErrNoOwnedClaim) {
		return fail(ctx, fmt.Sprintf("canceling run %s if claimed", in.RunID), err)
	}
	return nil
}

// FinalizeRunFailure commits a workflow-owned terminal failure.
func (a *TargetRecordingActivities) FinalizeRunFailure(ctx context.Context, in store.RunFailureInput) (store.TerminalResult, error) {
	result, err := a.store.FinalizeRunFailure(ctx, in)
	if err != nil {
		return store.TerminalResult{}, fail(ctx, fmt.Sprintf("finalizing failed run %s", in.RunID), err)
	}
	return result, nil
}
