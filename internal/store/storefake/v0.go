package storefake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// ClaimAndStartRun mirrors the target Store's atomic ownership boundary.
func (f *Store) ClaimAndStartRun(_ context.Context, in store.ClaimRunInput) (store.ClaimRunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ticket, ok := f.tickets[in.TicketID]
	if !ok {
		return store.ClaimRunResult{}, fmt.Errorf("ticket %d: %w", in.TicketID, store.ErrNotFound)
	}
	if ticket.State == store.TicketActive {
		if ticket.ActiveRunID != in.RunID {
			return store.ClaimRunResult{}, fmt.Errorf("ticket %d: %w", in.TicketID, store.ErrTicketClaimed)
		}
		return store.ClaimRunResult{Ticket: ticket, Run: f.runs[in.RunID]}, nil
	}
	if ticket.State != store.TicketOpen {
		return store.ClaimRunResult{}, fmt.Errorf("ticket %d: %w", in.TicketID, store.ErrTicketClaimed)
	}
	if existing, exists := f.runs[in.RunID]; exists && existing.TicketID != in.TicketID {
		return store.ClaimRunResult{}, fmt.Errorf("claiming ticket %d: run id belongs to another ticket: %w", in.TicketID, work.ErrPermanent)
	}
	ticket.State, ticket.ActiveRunID, ticket.UpdatedAt = store.TicketActive, in.RunID, f.clk.Now()
	f.tickets[in.TicketID] = ticket
	run := store.Run{ID: in.RunID, TicketID: in.TicketID, StartedAt: in.StartedAt}
	f.runs[in.RunID] = run
	return store.ClaimRunResult{Ticket: ticket, Run: run}, nil
}

// StartStep records an ordinal target Step exactly once.
func (f *Store) StartStep(_ context.Context, in store.StartStepInput) (store.RunStep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.targetRunOwnedLocked(in.RunID) {
		return store.RunStep{}, fmt.Errorf("starting step: %w", store.ErrRunOwnership)
	}
	k := targetStepKey{runID: in.RunID, ordinal: in.Ordinal}
	if step, ok := f.targetSteps[k]; ok {
		if step.Kind != in.Kind || step.Iteration != in.Iteration || step.Reason != in.Reason || !step.StartedAt.Equal(in.StartedAt) {
			return store.RunStep{}, fmt.Errorf("starting step: conflicting retry: %w", work.ErrPermanent)
		}
		return step, nil
	}
	step := store.RunStep{RunID: in.RunID, Ordinal: in.Ordinal, Kind: in.Kind, Iteration: in.Iteration, Reason: in.Reason, State: work.StepStateRunning, StartedAt: in.StartedAt}
	f.targetSteps[k] = step
	return step, nil
}

// CompleteStep completes a target Step with its durable result.
func (f *Store) CompleteStep(_ context.Context, runID string, ordinal int, endedAt time.Time, result json.RawMessage) (store.RunStep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.targetRunOwnedLocked(runID) {
		return store.RunStep{}, fmt.Errorf("completing step: %w", store.ErrRunOwnership)
	}
	k := targetStepKey{runID: runID, ordinal: ordinal}
	step, ok := f.targetSteps[k]
	if !ok {
		return store.RunStep{}, fmt.Errorf("step %d: %w", ordinal, store.ErrNotFound)
	}
	if step.State != work.StepStateRunning {
		if step.State == work.StepStateCompleted && jsonEqual(step.Result, result) {
			return step, nil
		}
		return store.RunStep{}, fmt.Errorf("completing step: conflicting retry: %w", work.ErrPermanent)
	}
	step.State, step.EndedAt, step.Result = work.StepStateCompleted, endedAt, result
	f.targetSteps[k] = step
	return step, nil
}

// TargetRunDetail reads target Steps and their Agent Attempts in durable order.
func (f *Store) TargetRunDetail(_ context.Context, runID string) (store.TargetRunDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return store.TargetRunDetail{}, fmt.Errorf("run %s: %w", runID, store.ErrNotFound)
	}
	steps := make([]store.TargetStepDetail, 0)
	for key, step := range f.targetSteps {
		if key.runID == runID {
			steps = append(steps, store.TargetStepDetail{Step: step})
		}
	}
	sort.Slice(steps, func(left, right int) bool {
		return steps[left].Step.Ordinal < steps[right].Step.Ordinal
	})
	for index := range steps {
		for id, attempt := range f.targetAttempts {
			if id.RunID == runID && id.StepOrdinal == steps[index].Step.Ordinal {
				steps[index].Attempts = append(steps[index].Attempts, attempt)
			}
		}
		sort.Slice(steps[index].Attempts, func(left, right int) bool {
			return steps[index].Attempts[left].ID.AttemptNo < steps[index].Attempts[right].ID.AttemptNo
		})
	}
	return store.TargetRunDetail{Run: run, Steps: steps}, nil
}

// TargetTranscript reads target transcript evidence without a write capability.
func (f *Store) TargetTranscript(_ context.Context, id store.TargetAttemptID) (store.TargetTranscript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	transcript, ok := f.targetTranscripts[id]
	if !ok {
		return store.TargetTranscript{}, fmt.Errorf("reading target transcript %s: %w", id, store.ErrNotFound)
	}
	return transcript, nil
}

// StartAgentAttempt records one agent execution under an existing target Step.
func (f *Store) StartAgentAttempt(_ context.Context, in store.StartAgentAttemptInput) (store.AgentAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.targetRunOwnedLocked(in.ID.RunID) {
		return store.AgentAttempt{}, fmt.Errorf("starting agent attempt: %w", store.ErrRunOwnership)
	}
	step, ok := f.targetSteps[targetStepKey{runID: in.ID.RunID, ordinal: in.ID.StepOrdinal}]
	if !ok {
		return store.AgentAttempt{}, fmt.Errorf("starting agent attempt %s: %w", in.ID, store.ErrAgentAttemptStep)
	}
	if attempt, ok := f.targetAttempts[in.ID]; ok {
		if step.State != work.StepStateRunning || attempt.AgentStage != in.AgentStage || attempt.Model != in.Model || attempt.UsageState != in.UsageState || !attempt.StartedAt.Equal(in.StartedAt) {
			return store.AgentAttempt{}, fmt.Errorf("starting agent attempt: conflicting retry: %w", work.ErrPermanent)
		}
		return attempt, nil
	}
	if step.State != work.StepStateRunning || !in.AgentStage.MatchesStep(step.Kind) {
		return store.AgentAttempt{}, fmt.Errorf("starting agent attempt %s: %w", in.ID, store.ErrAgentAttemptStep)
	}
	attempt := store.AgentAttempt{ID: in.ID, AgentStage: in.AgentStage, Model: in.Model, State: work.AgentAttemptRunning, UsageState: in.UsageState, StartedAt: in.StartedAt}
	f.targetAttempts[in.ID] = attempt
	return attempt, nil
}

// BindCheckpointCapability binds one capability to one exact active Attempt.
func (f *Store) BindCheckpointCapability(_ context.Context, attemptID store.TargetAttemptID, capability string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.targetRunOwnedLocked(attemptID.RunID) {
		return fmt.Errorf("binding checkpoint capability: %w", store.ErrRunOwnership)
	}
	attempt, ok := f.targetAttempts[attemptID]
	if !ok {
		return fmt.Errorf("attempt %s: %w", attemptID, store.ErrNotFound)
	}
	if attempt.State != work.AgentAttemptRunning || (f.capabilityHash[attemptID] != "" && f.capabilityHash[attemptID] != capability) {
		return fmt.Errorf("binding checkpoint capability: conflicting retry: %w", work.ErrPermanent)
	}
	f.capabilityHash[attemptID] = capability
	return nil
}

// FailAgentAttempt mirrors the main workflow's authority to close one
// exhausted execution before it may authorize a replacement Attempt.
func (f *Store) FailAgentAttempt(_ context.Context, in store.AgentAttemptFailureInput) (store.AgentAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.FailureKind == "" || in.EndedAt.IsZero() {
		return store.AgentAttempt{}, fmt.Errorf("failing agent attempt %s: failure kind and terminal time are required: %w", in.ID, work.ErrPermanent)
	}
	if !f.targetRunOwnedLocked(in.ID.RunID) {
		return store.AgentAttempt{}, fmt.Errorf("failing agent attempt: %w", store.ErrRunOwnership)
	}
	attempt, ok := f.targetAttempts[in.ID]
	if !ok {
		return store.AgentAttempt{}, fmt.Errorf("attempt %s: %w", in.ID, store.ErrNotFound)
	}
	if attempt.State != work.AgentAttemptRunning {
		if attempt.State != work.AgentAttemptFailed || attempt.FailureKind != in.FailureKind || !attempt.EndedAt.Equal(in.EndedAt) {
			return store.AgentAttempt{}, fmt.Errorf("failing agent attempt: conflicting terminal failure: %w", work.ErrPermanent)
		}
		return attempt, nil
	}
	attempt.State, attempt.FailureKind, attempt.EndedAt = work.AgentAttemptFailed, in.FailureKind, in.EndedAt
	f.targetAttempts[in.ID] = attempt
	return attempt, nil
}

// CheckpointAgentAttempt writes only an Attempt owned by the supplied capability.
func (f *Store) CheckpointAgentAttempt(_ context.Context, in store.AgentCheckpointInput) (store.AgentAttempt, error) {
	return f.checkpointAgentAttempt(in, true)
}

// FinalizeAgentWorkflowAttempt mirrors the main-control immutable evidence path.
func (f *Store) FinalizeAgentWorkflowAttempt(_ context.Context, in store.AgentCheckpointInput) (store.AgentAttempt, error) {
	return f.checkpointAgentAttempt(in, false)
}

func (f *Store) checkpointAgentAttempt(in store.AgentCheckpointInput, requireCapability bool) (store.AgentAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := in.Validate(); err != nil {
		return store.AgentAttempt{}, err
	}
	if !f.targetRunOwnedLocked(in.ID.RunID) {
		return store.AgentAttempt{}, fmt.Errorf("checkpoint: %w", store.ErrRunOwnership)
	}
	if requireCapability && f.capabilityHash[in.ID] != in.Capability {
		return store.AgentAttempt{}, fmt.Errorf("checkpoint: %w", store.ErrRunOwnership)
	}
	attempt, ok := f.targetAttempts[in.ID]
	if !ok {
		return store.AgentAttempt{}, fmt.Errorf("attempt %s: %w", in.ID, store.ErrNotFound)
	}
	if attempt.State != work.AgentAttemptRunning {
		if !terminalAgentCheckpointMatches(attempt, in) || (in.Transcript != nil && !targetTranscriptMatches(f.targetTranscripts[in.ID], in.Transcript)) {
			return store.AgentAttempt{}, fmt.Errorf("checkpoint: conflicting terminal checkpoint: %w", work.ErrPermanent)
		}
		return attempt, nil
	}
	if in.State == work.AgentAttemptRunning && attempt.ExecutionID != "" {
		if !runningAgentCheckpointMatches(attempt, in) || !targetTranscriptMatches(f.targetTranscripts[in.ID], in.Transcript) {
			return store.AgentAttempt{}, fmt.Errorf("checkpoint: conflicting running checkpoint: %w", work.ErrPermanent)
		}
		return attempt, nil
	}
	if in.Transcript != nil {
		f.targetTranscripts[in.ID] = *in.Transcript
	}
	attempt.ExecutionID, attempt.State, attempt.FailureKind, attempt.UsageState, attempt.Usage, attempt.EndedAt, attempt.Result = in.ExecutionID, in.State, in.FailureKind, in.UsageState, in.Usage, in.EndedAt, in.Result
	_, attempt.TranscriptPresent = f.targetTranscripts[in.ID]
	f.targetAttempts[in.ID] = attempt
	return attempt, nil
}

// LoadAgentCheckpoint authenticates the exact Attempt before returning its durable evidence.
func (f *Store) LoadAgentCheckpoint(_ context.Context, id store.TargetAttemptID, capability string) (store.AgentAttempt, *store.TargetTranscript, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	attempt, ok := f.targetAttempts[id]
	if !ok || f.capabilityHash[id] != capability {
		return store.AgentAttempt{}, nil, false, fmt.Errorf("loading checkpoint: %w", store.ErrRunOwnership)
	}
	if attempt.State == work.AgentAttemptRunning && attempt.ExecutionID == "" {
		return attempt, nil, false, nil
	}
	var transcript *store.TargetTranscript
	if stored, exists := f.targetTranscripts[id]; exists {
		copy := stored
		transcript = &copy
	}
	return attempt, transcript, true, nil
}

func terminalAgentCheckpointMatches(attempt store.AgentAttempt, in store.AgentCheckpointInput) bool {
	return attempt.State == in.State &&
		attempt.ExecutionID == in.ExecutionID &&
		attempt.FailureKind == in.FailureKind &&
		attempt.UsageState == in.UsageState &&
		attempt.Usage == in.Usage &&
		attempt.EndedAt.Equal(in.EndedAt) &&
		jsonEqual(attempt.Result, in.Result)
}

func runningAgentCheckpointMatches(attempt store.AgentAttempt, in store.AgentCheckpointInput) bool {
	return terminalAgentCheckpointMatches(attempt, in) && in.State == work.AgentAttemptRunning && in.EndedAt.IsZero() && len(in.Result) == 0
}

func targetTranscriptMatches(current store.TargetTranscript, in *store.TargetTranscript) bool {
	if in == nil {
		return len(current.CompressedBytes) == 0 && current.Compression == "" && current.UncompressedSizeBytes == 0 && len(current.Checksum) == 0
	}
	return bytes.Equal(current.CompressedBytes, in.CompressedBytes) && current.Compression == in.Compression && current.UncompressedSizeBytes == in.UncompressedSizeBytes && bytes.Equal(current.Checksum, in.Checksum)
}

func jsonEqual(left, right json.RawMessage) bool {
	if !json.Valid(left) || !json.Valid(right) {
		return bytes.Equal(left, right)
	}
	var leftValue interface{}
	var rightValue interface{}
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// CheckpointGitEffect stores a monotonic repository recovery checkpoint.
func (f *Store) CheckpointGitEffect(_ context.Context, in store.GitCheckpointInput) (store.GitCheckpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkpointGitEffectLocked(in, true)
}

// LatestCanceledRunCheckpoint mirrors the production recovery fence: only a
// canceled predecessor of the same Ticket can donate a durable pushed head.
func (f *Store) LatestCanceledRunCheckpoint(_ context.Context, ticketID store.TicketID, excludingRunID string) (store.CanceledRunRecovery, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var chosen store.Run
	found := false
	for _, run := range f.runs {
		if run.ID == excludingRunID || run.TicketID != ticketID || run.TargetOutcome != work.RunOutcomeCanceled {
			continue
		}
		checkpoint, exists := f.targetGit[run.ID]
		if !exists || checkpoint.PushedHead == "" {
			continue
		}
		if !found || run.EndedAt.After(chosen.EndedAt) || (run.EndedAt.Equal(chosen.EndedAt) && run.ID > chosen.ID) {
			chosen, found = run, true
		}
	}
	if !found {
		return store.CanceledRunRecovery{}, false, nil
	}
	mergeOrdinal := 0
	for key, step := range f.targetSteps {
		if key.runID == chosen.ID && step.Kind == work.StepMergePullRequest && step.State == work.StepStateRunning && key.ordinal > mergeOrdinal {
			mergeOrdinal = key.ordinal
		}
	}
	return store.CanceledRunRecovery{Checkpoint: f.targetGit[chosen.ID], MergeStepOrdinal: mergeOrdinal}, true, nil
}

// BindRepositoryCapability installs one monotonically increasing Run Worker
// generation as the repository checkpoint owner.
func (f *Store) BindRepositoryCapability(_ context.Context, identity work.RunWorkerIdentity, capability string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := identity.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(capability) == "" {
		return fmt.Errorf("binding repository capability: capability is empty: %w", work.ErrPermanent)
	}
	if !f.targetRunOwnedLocked(identity.RunID) {
		return fmt.Errorf("binding repository capability: %w", store.ErrRunOwnership)
	}
	current, exists := f.repositoryCapability[identity.RunID]
	switch {
	case !exists, current.generation < identity.Generation:
		f.repositoryCapability[identity.RunID] = repositoryCapability{generation: identity.Generation, value: capability}
		return nil
	case current.generation == identity.Generation && current.value == capability:
		return nil
	default:
		return fmt.Errorf("binding repository capability: conflicting or obsolete generation: %w", work.ErrPermanent)
	}
}

// LoadRepositoryCheckpoint returns only the current generation's position.
func (f *Store) LoadRepositoryCheckpoint(_ context.Context, identity work.RunWorkerIdentity, capability string) (store.GitCheckpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := identity.Validate(); err != nil {
		return store.GitCheckpoint{}, false, err
	}
	if !f.repositoryCapabilityMatchesLocked(identity, capability) {
		return store.GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: %w", store.ErrRunOwnership)
	}
	checkpoint, found := f.targetGit[identity.RunID]
	return checkpoint, found, nil
}

// CheckpointRepository authenticates the generation before using the same
// atomic Step/checkpoint transition as the privileged compatibility path.
func (f *Store) CheckpointRepository(_ context.Context, in store.RepositoryCheckpointInput) (store.GitCheckpoint, error) {
	return f.checkpointRepository(in, true)
}

// CheckpointRepositoryEffect preserves an external result for terminal
// finalization without completing the owning Step.
func (f *Store) CheckpointRepositoryEffect(_ context.Context, in store.RepositoryCheckpointInput) (store.GitCheckpoint, error) {
	return f.checkpointRepository(in, false)
}

func (f *Store) checkpointRepository(in store.RepositoryCheckpointInput, completeStep bool) (store.GitCheckpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := in.Identity.Validate(); err != nil {
		return store.GitCheckpoint{}, err
	}
	if in.GitCheckpoint.RunID != in.Identity.RunID || !f.repositoryCapabilityMatchesLocked(in.Identity, in.Capability) {
		return store.GitCheckpoint{}, fmt.Errorf("checkpointing repository effect: %w", store.ErrRunOwnership)
	}
	return f.checkpointGitEffectLocked(store.GitCheckpointInput{GitCheckpoint: in.GitCheckpoint, CompletedAt: in.CompletedAt}, completeStep)
}

func (f *Store) repositoryCapabilityMatchesLocked(identity work.RunWorkerIdentity, capability string) bool {
	current, exists := f.repositoryCapability[identity.RunID]
	return exists && f.targetRunOwnedLocked(identity.RunID) && current.generation == identity.Generation && current.value == capability
}

func (f *Store) checkpointGitEffectLocked(in store.GitCheckpointInput, completeStep bool) (store.GitCheckpoint, error) {
	if !f.targetRunOwnedLocked(in.RunID) {
		return store.GitCheckpoint{}, fmt.Errorf("checkpoint: %w", store.ErrRunOwnership)
	}
	k := targetStepKey{runID: in.RunID, ordinal: in.StepOrdinal}
	step, stepExists := f.targetSteps[k]
	if previous, ok := f.targetGit[in.RunID]; ok {
		if previous.StepOrdinal > in.StepOrdinal || (previous.StepOrdinal == in.StepOrdinal && !gitCheckpointMatches(previous, in.GitCheckpoint)) {
			return store.GitCheckpoint{}, fmt.Errorf("checkpoint: %w", work.ErrPermanent)
		}
		if previous.StepOrdinal == in.StepOrdinal {
			if !stepExists || (completeStep && (step.State != work.StepStateCompleted || !jsonEqual(step.Result, in.StepResult))) || (!completeStep && step.State != work.StepStateRunning) {
				return store.GitCheckpoint{}, fmt.Errorf("checkpoint: conflicting completed step: %w", work.ErrPermanent)
			}
			return previous, nil
		}
	}
	if !stepExists {
		return store.GitCheckpoint{}, fmt.Errorf("checkpoint step %d: %w", in.StepOrdinal, store.ErrNotFound)
	}
	if step.State != work.StepStateRunning {
		return store.GitCheckpoint{}, fmt.Errorf("checkpoint: step is not running: %w", work.ErrPermanent)
	}
	f.targetGit[in.RunID] = in.GitCheckpoint
	if completeStep {
		step.State, step.EndedAt, step.Result = work.StepStateCompleted, in.CompletedAt, in.StepResult
		f.targetSteps[k] = step
	}
	return in.GitCheckpoint, nil
}

// FinalizeConfirmedMerge commits the irreversible target terminal outcome.
func (f *Store) FinalizeConfirmedMerge(_ context.Context, in store.ConfirmedMergeInput) (store.TerminalResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[in.RunID]
	if !ok || run.TicketID != in.TicketID {
		return store.TerminalResult{}, fmt.Errorf("merge: %w", store.ErrRunOwnership)
	}
	ticket := f.tickets[in.TicketID]
	if run.TargetOutcome != "" && run.TargetOutcome != work.RunOutcomeCanceled {
		if run.TargetOutcome != work.RunOutcomeSucceeded || run.MergeSHA != in.MergeSHA || run.ReviewedHead != in.ReviewedHead {
			return store.TerminalResult{}, fmt.Errorf("merge: %w", work.ErrPermanent)
		}
		return store.TerminalResult{Ticket: f.tickets[in.TicketID], Run: run}, nil
	}
	if in.StepOrdinal == 0 && run.TargetOutcome == work.RunOutcomeCanceled {
		for key := range f.targetSteps {
			if key.runID == in.RunID && key.ordinal >= in.StepOrdinal {
				in.StepOrdinal = key.ordinal + 1
			}
		}
		f.targetSteps[targetStepKey{runID: in.RunID, ordinal: in.StepOrdinal}] = store.RunStep{
			RunID: in.RunID, Ordinal: in.StepOrdinal, Kind: work.StepMergePullRequest,
			Reason: "reconcile confirmed external merge", State: work.StepStateRunning, StartedAt: in.EndedAt,
		}
	}
	stepKey := targetStepKey{runID: in.RunID, ordinal: in.StepOrdinal}
	step, ok := f.targetSteps[stepKey]
	if !ok || step.Kind != work.StepMergePullRequest || step.State != work.StepStateRunning {
		return store.TerminalResult{}, fmt.Errorf("merge: %w", store.ErrMergeStep)
	}
	var successorRunID string
	if run.TargetOutcome == work.RunOutcomeCanceled {
		switch {
		case ticket.State == store.TicketOpen && ticket.ActiveRunID == "":
		case ticket.State == store.TicketActive && ticket.ActiveRunID != "" && ticket.ActiveRunID != in.RunID:
			successor, successorExists := f.runs[ticket.ActiveRunID]
			if !successorExists || successor.TicketID != in.TicketID || successor.TargetOutcome != "" {
				return store.TerminalResult{}, fmt.Errorf("merge: %w", store.ErrRunOwnership)
			}
			successorRunID = ticket.ActiveRunID
		default:
			return store.TerminalResult{}, fmt.Errorf("merge: %w", store.ErrRunOwnership)
		}
	} else if ticket.State != store.TicketActive || ticket.ActiveRunID != in.RunID {
		return store.TerminalResult{}, fmt.Errorf("merge: %w", store.ErrRunOwnership)
	}
	result, err := json.Marshal(struct {
		Kind     string `json:"kind"`
		MergeSHA string `json:"merge_sha"`
	}{Kind: "merged", MergeSHA: in.MergeSHA})
	if err != nil {
		return store.TerminalResult{}, fmt.Errorf("encoding confirmed merge result: %w", err)
	}
	step.State, step.EndedAt, step.Result = work.StepStateCompleted, in.EndedAt, result
	f.targetSteps[stepKey] = step
	if run.TargetOutcome == work.RunOutcomeCanceled {
		if successorRunID != "" {
			successor := f.runs[successorRunID]
			successor.TargetOutcome, successor.EndedAt = work.RunOutcomeCanceled, in.EndedAt
			f.runs[successorRunID] = successor
		}
		run.TargetOutcome, run.ReviewedHead, run.MergeSHA, run.EndedAt = work.RunOutcomeSucceeded, in.ReviewedHead, in.MergeSHA, in.EndedAt
		ticket.State, ticket.ActiveRunID, ticket.UpdatedAt = store.TicketDone, "", f.clk.Now()
		f.runs[in.RunID], f.tickets[in.TicketID] = run, ticket
		return store.TerminalResult{Ticket: ticket, Run: run}, nil
	}
	run.TargetOutcome, run.ReviewedHead, run.MergeSHA, run.EndedAt = work.RunOutcomeSucceeded, in.ReviewedHead, in.MergeSHA, in.EndedAt
	ticket.State, ticket.ActiveRunID, ticket.UpdatedAt = store.TicketDone, "", f.clk.Now()
	f.runs[in.RunID], f.tickets[in.TicketID] = run, ticket
	return store.TerminalResult{Ticket: ticket, Run: run}, nil
}

// CancelRun conditionally returns an unmerged owned Ticket to open.
func (f *Store) CancelRun(_ context.Context, in store.CancelRunInput) (store.TerminalResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[in.RunID]
	if !ok {
		return store.TerminalResult{}, fmt.Errorf("cancel: %w", store.ErrNoOwnedClaim)
	}
	if run.TicketID != in.TicketID {
		return store.TerminalResult{}, fmt.Errorf("cancel: %w", store.ErrRunOwnership)
	}
	ticket := f.tickets[in.TicketID]
	if run.TargetOutcome == work.RunOutcomeCanceled {
		if ticket.State != store.TicketOpen || ticket.ActiveRunID != "" {
			return store.TerminalResult{}, fmt.Errorf("cancel: %w", store.ErrRunOwnership)
		}
		return store.TerminalResult{Ticket: ticket, Run: run}, nil
	}
	if run.TargetOutcome != "" {
		return store.TerminalResult{Ticket: ticket, Run: run}, nil
	}
	if ticket.State != store.TicketActive || ticket.ActiveRunID != in.RunID {
		return store.TerminalResult{}, fmt.Errorf("cancel: %w", store.ErrRunOwnership)
	}
	run.TargetOutcome, run.EndedAt = work.RunOutcomeCanceled, in.EndedAt
	ticket.State, ticket.ActiveRunID, ticket.UpdatedAt = store.TicketOpen, "", f.clk.Now()
	f.tickets[in.TicketID], f.runs[in.RunID] = ticket, run
	return store.TerminalResult{Ticket: f.tickets[in.TicketID], Run: f.runs[in.RunID]}, nil
}

// FinalizeRunFailure conditionally records a terminal workflow failure.
func (f *Store) FinalizeRunFailure(_ context.Context, in store.RunFailureInput) (store.TerminalResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[in.RunID]
	if !ok || run.TicketID != in.TicketID {
		return store.TerminalResult{}, fmt.Errorf("failure: %w", store.ErrRunOwnership)
	}
	ticket := f.tickets[in.TicketID]
	if run.TargetOutcome != "" {
		if (run.TargetOutcome != in.Outcome || run.TargetFailure != in.FailureKind) && run.TargetOutcome != work.RunOutcomeSucceeded && run.TargetOutcome != work.RunOutcomeCanceled {
			return store.TerminalResult{}, fmt.Errorf("failure: %w", work.ErrPermanent)
		}
		return store.TerminalResult{Ticket: ticket, Run: run}, nil
	}
	if ticket.State != store.TicketActive || ticket.ActiveRunID != in.RunID {
		return store.TerminalResult{}, fmt.Errorf("failure: %w", store.ErrRunOwnership)
	}
	if in.Outcome != work.RunOutcomeFailed && in.Outcome != work.RunOutcomeExhausted {
		return store.TerminalResult{}, fmt.Errorf("failure: %w", work.ErrPermanent)
	}
	if in.StepOrdinal > 0 {
		key := targetStepKey{runID: in.RunID, ordinal: in.StepOrdinal}
		step, exists := f.targetSteps[key]
		if !exists || step.State != work.StepStateRunning {
			return store.TerminalResult{}, fmt.Errorf("failure: %w", store.ErrRunOwnership)
		}
		for attemptKey, attempt := range f.targetAttempts {
			if attemptKey.RunID == in.RunID && attemptKey.StepOrdinal == in.StepOrdinal && attempt.State == work.AgentAttemptRunning {
				attempt.State, attempt.FailureKind, attempt.EndedAt = work.AgentAttemptFailed, in.FailureKind, in.EndedAt
				f.targetAttempts[attemptKey] = attempt
			}
		}
		step.State, step.EndedAt, step.Result = work.StepStateFailed, in.EndedAt, in.StepResult
		f.targetSteps[key] = step
	}
	run.TargetOutcome, run.TargetFailure, run.EndedAt = in.Outcome, in.FailureKind, in.EndedAt
	ticket.State, ticket.ActiveRunID, ticket.UpdatedAt = store.TicketFailed, "", f.clk.Now()
	f.runs[in.RunID], f.tickets[in.TicketID] = run, ticket
	return store.TerminalResult{Ticket: ticket, Run: run}, nil
}

// ReconcileAbandonedRun releases only nonterminal ownership without inventing an outcome.
func (f *Store) ReconcileAbandonedRun(_ context.Context, runID string, ticketID store.TicketID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || run.TicketID != ticketID {
		return false, nil
	}
	if run.TargetOutcome != "" {
		return false, fmt.Errorf("reconcile: %w", store.ErrRunOwnership)
	}
	ticket := f.tickets[ticketID]
	if ticket.State != store.TicketActive || ticket.ActiveRunID != runID {
		return false, nil
	}
	ticket.State, ticket.ActiveRunID, ticket.UpdatedAt = store.TicketOpen, "", f.clk.Now()
	f.tickets[ticketID] = ticket
	return true, nil
}

func (f *Store) targetRunOwnedLocked(runID string) bool {
	run, ok := f.runs[runID]
	if !ok || run.TargetOutcome != "" {
		return false
	}
	ticket, ok := f.tickets[run.TicketID]
	return ok && ticket.State == store.TicketActive && ticket.ActiveRunID == runID
}

func gitCheckpointMatches(current, in store.GitCheckpoint) bool {
	return current.StepOrdinal == in.StepOrdinal && current.Branch == in.Branch && current.PushedHead == in.PushedHead && current.ObservedBase == in.ObservedBase && current.PullRequestNumber == in.PullRequestNumber && current.PullRequestNodeID == in.PullRequestNodeID && jsonEqual(current.StepResult, in.StepResult)
}

var (
	_ store.TargetRunClaimer       = (*Store)(nil)
	_ store.TargetStepRecorder     = (*Store)(nil)
	_ store.TargetAgentRecorder    = (*Store)(nil)
	_ store.TargetTerminalRecorder = (*Store)(nil)
)
