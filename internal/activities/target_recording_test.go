package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/work"
)

type targetRecorderProbe struct {
	*storefake.Store
	completeErr error
	gitErr      error

	completeRunID   string
	completeOrdinal int
	completeEndedAt time.Time
	completeResult  json.RawMessage
	gitInput        store.GitCheckpointInput
}

func (p *targetRecorderProbe) CompleteStep(ctx context.Context, runID string, ordinal int, endedAt time.Time, result json.RawMessage) (store.RunStep, error) {
	p.completeRunID, p.completeOrdinal, p.completeEndedAt, p.completeResult = runID, ordinal, endedAt, result
	if p.completeErr != nil {
		return store.RunStep{}, p.completeErr
	}
	return p.Store.CompleteStep(ctx, runID, ordinal, endedAt, result)
}

func (p *targetRecorderProbe) CheckpointGitEffect(ctx context.Context, in store.GitCheckpointInput) (store.GitCheckpoint, error) {
	p.gitInput = in
	if p.gitErr != nil {
		return store.GitCheckpoint{}, p.gitErr
	}
	return p.Store.CheckpointGitEffect(ctx, in)
}

func mustNewTargetRecording(t *testing.T, recorder TargetRunRecorder) *TargetRecordingActivities {
	t.Helper()
	activities, err := NewTargetRecordingActivities(recorder)
	if err != nil {
		t.Fatalf("NewTargetRecordingActivities: %v", err)
	}
	return activities
}

func TestTargetRecordingActivitiesForwardStepCompletionAndGitCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	probe := &targetRecorderProbe{Store: storefake.New()}
	activities := mustNewTargetRecording(t, probe)
	startedAt := fixedTestTime
	ticket, err := probe.CreateTicket(ctx, "target recorder", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := uuid.NewString()
	if _, err := probe.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := probe.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepPlan, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep(plan): %v", err)
	}
	endedAt := startedAt.Add(time.Minute)
	result := json.RawMessage(`{"kind":"planned"}`)
	if _, err := activities.CompleteStep(ctx, runID, 1, endedAt, result); err != nil {
		t.Fatalf("CompleteStep: %v", err)
	}
	if probe.completeRunID != runID || probe.completeOrdinal != 1 || !probe.completeEndedAt.Equal(endedAt) || !bytes.Equal(probe.completeResult, result) {
		t.Fatalf("CompleteStep forwarded (%q, %d, %s, %s), want (%q, 1, %s, %s)", probe.completeRunID, probe.completeOrdinal, probe.completeEndedAt, probe.completeResult, runID, endedAt, result)
	}

	if _, err := probe.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 2, Kind: work.StepSyncPullRequest, StartedAt: endedAt}); err != nil {
		t.Fatalf("StartStep(git): %v", err)
	}
	checkpoint := store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{RunID: runID, StepOrdinal: 2, Branch: "factory/adapter", PushedHead: "head", ObservedBase: "base", PullRequestNumber: 42, PullRequestNodeID: "node", StepResult: json.RawMessage(`{"kind":"synced"}`)}, CompletedAt: endedAt.Add(time.Minute)}
	if _, err := activities.CheckpointGitEffect(ctx, checkpoint); err != nil {
		t.Fatalf("CheckpointGitEffect: %v", err)
	}
	if probe.gitInput.RunID != checkpoint.RunID || probe.gitInput.StepOrdinal != checkpoint.StepOrdinal || probe.gitInput.PushedHead != checkpoint.PushedHead {
		t.Fatalf("CheckpointGitEffect forwarded %+v, want %+v", probe.gitInput, checkpoint)
	}
}

func TestTargetRecordingActivitiesPropagateMandatoryRecorderFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	completeFailure := errors.New("complete step failed")
	gitFailure := errors.New("git checkpoint failed")
	probe := &targetRecorderProbe{Store: storefake.New(), completeErr: completeFailure, gitErr: gitFailure}
	activities := mustNewTargetRecording(t, probe)

	if _, err := activities.CompleteStep(ctx, uuid.NewString(), 1, fixedTestTime, json.RawMessage(`{}`)); !errors.Is(err, completeFailure) {
		t.Fatalf("CompleteStep error = %v, want wrapped recorder failure", err)
	}
	if _, err := activities.CheckpointGitEffect(ctx, store.GitCheckpointInput{}); !errors.Is(err, gitFailure) {
		t.Fatalf("CheckpointGitEffect error = %v, want wrapped recorder failure", err)
	}
}

func TestTargetRecordingActivitiesCancelRunIfClaimedToleratesAMissingClaim(t *testing.T) {
	t.Parallel()
	activities := mustNewTargetRecording(t, storefake.New())
	if err := activities.CancelRunIfClaimed(context.Background(), store.CancelRunInput{
		RunID: uuid.NewString(), TicketID: 1, EndedAt: fixedTestTime,
	}); err != nil {
		t.Fatalf("CancelRunIfClaimed without committed claim: %v", err)
	}
}
