package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/0x63616c/software-factory/internal/store/storedb"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrTicketClaimed reports that another target Run owns a Ticket.
var ErrTicketClaimed = errors.New("ticket already has an active run")

// ErrRunOwnership reports a terminal operation naming a stale owner.
var ErrRunOwnership = errors.New("run does not own ticket")

// ErrNoOwnedClaim reports that a conditional cancellation found no Run claim.
var ErrNoOwnedClaim = errors.New("run claim does not exist")

// ErrActiveTicketOwnership reports an attempt to create target ownership outside
// ClaimAndStartRun.
var ErrActiveTicketOwnership = errors.New("active ticket ownership is store-managed")

// ErrAgentAttemptStep reports an Agent Attempt whose parent is not its matching
// agent-backed Step.
var ErrAgentAttemptStep = errors.New("agent attempt requires matching agent step")

// ErrMergeStep reports confirmed merge evidence without its running Merge Step.
var ErrMergeStep = errors.New("confirmed merge requires running merge step")

var errConfirmedMergeOwnerChanged = errors.New("confirmed merge ticket owner changed while locking")

const confirmedMergeLockAttempts = 8

// TargetRunClaimer is the target workflow's atomic admission boundary.
type TargetRunClaimer interface {
	ClaimAndStartRun(context.Context, ClaimRunInput) (ClaimRunResult, error)
}

// CanceledRunRecoveryReader returns only the non-secret Git position that a
// newly claimed Run may carry forward from an earlier canceled Run for the
// same Ticket. A confirmed merge never satisfies this boundary.
type CanceledRunRecoveryReader interface {
	LatestCanceledRunCheckpoint(context.Context, TicketID, string) (CanceledRunRecovery, bool, error)
}

// TargetStepRecorder records mandatory target Step lifecycle boundaries.
type TargetStepRecorder interface {
	StartStep(context.Context, StartStepInput) (RunStep, error)
	CompleteStep(context.Context, string, int, time.Time, json.RawMessage) (RunStep, error)
}

// TargetAgentRecorder records durable agent authorization and checkpoint boundaries.
type TargetAgentRecorder interface {
	StartAgentAttempt(context.Context, StartAgentAttemptInput) (AgentAttempt, error)
	FailAgentAttempt(context.Context, AgentAttemptFailureInput) (AgentAttempt, error)
	CheckpointAgentAttempt(context.Context, AgentCheckpointInput) (AgentAttempt, error)
}

// TargetTerminalRecorder records irreversible and cancellation outcomes.
type TargetTerminalRecorder interface {
	FinalizeConfirmedMerge(context.Context, ConfirmedMergeInput) (TerminalResult, error)
	CancelRun(context.Context, CancelRunInput) (TerminalResult, error)
	FinalizeRunFailure(context.Context, RunFailureInput) (TerminalResult, error)
}

// LegacyTicketCount proves the final target-state migration has no old rows.
func (s *Store) LegacyTicketCount(ctx context.Context) (int64, error) {
	count, err := s.q.CountLegacyTicketStates(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting pre-activation Ticket states: %w", wrapQueryErr(err))
	}
	return count, nil
}

// TargetHistoryReader exposes the durable target projection used by the console.
type TargetHistoryReader interface {
	TargetRunDetail(context.Context, string) (TargetRunDetail, error)
	TargetTranscript(context.Context, TargetAttemptID) (TargetTranscript, error)
}

// ClaimRunInput is the stable identity for an atomic target claim.
type ClaimRunInput struct {
	TicketID  TicketID
	RunID     string
	StartedAt time.Time
}

// ClaimRunResult is the Ticket and Run committed by one target claim.
type ClaimRunResult struct {
	Ticket Ticket
	Run    Run
}

// ClaimAndStartRun atomically claims an open Ticket and creates its owning Run.
// Repeating the same identity returns the original owner; a different Run never
// observes partial ownership or creates a second durable Run.
func (s *Store) ClaimAndStartRun(ctx context.Context, in ClaimRunInput) (ClaimRunResult, error) {
	if s.begin == nil {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: store cannot begin a transaction", in.TicketID)
	}
	runID, err := pgUUID(in.RunID)
	if err != nil {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: %w", in.TicketID, err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: beginning transaction: %w", in.TicketID, wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	ticketRow, err := q.TicketForTargetClaim(ctx, int64(in.TicketID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: %w", in.TicketID, ErrNotFound)
		}
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: %w", in.TicketID, wrapQueryErr(err))
	}
	ticket, err := ticketFromRow(ticketRow)
	if err != nil {
		return ClaimRunResult{}, err
	}
	if ticket.State == TicketActive {
		if ticket.ActiveRunID != in.RunID {
			return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: %w", in.TicketID, ErrTicketClaimed)
		}
		runRow, runErr := q.TargetRunForUpdate(ctx, runID)
		if runErr != nil {
			return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: reading retried run: %w", in.TicketID, wrapQueryErr(runErr))
		}
		if runRow.TicketID != int64(in.TicketID) {
			return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: %w", in.TicketID, work.ErrPermanent)
		}
		if err := tx.Commit(ctx); err != nil {
			return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: committing retry: %w", in.TicketID, wrapQueryErr(err))
		}
		return ClaimRunResult{Ticket: ticket, Run: runFromRow(runRow)}, nil
	}
	if ticket.State != TicketOpen {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d in %s: %w", in.TicketID, ticket.State, ErrTicketClaimed)
	}
	runRow, err := q.InsertTargetRun(ctx, storedb.InsertTargetRunParams{ID: runID, TicketID: int64(in.TicketID), StartedAt: pgTimestamp(in.StartedAt)})
	if err != nil {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: inserting run: %w", in.TicketID, wrapQueryErr(err))
	}
	if runRow.TicketID != int64(in.TicketID) {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: run id belongs to another ticket: %w", in.TicketID, work.ErrPermanent)
	}
	activeRow, err := q.ActivateTargetTicket(ctx, storedb.ActivateTargetTicketParams{ID: int64(in.TicketID), ActiveRunID: runID})
	if err != nil {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: activating: %w", in.TicketID, wrapQueryErr(err))
	}
	active, err := ticketFromRow(activeRow)
	if err != nil {
		return ClaimRunResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimRunResult{}, fmt.Errorf("claiming ticket %d: committing: %w", in.TicketID, wrapQueryErr(err))
	}
	return ClaimRunResult{Ticket: active, Run: runFromRow(runRow)}, nil
}

// RunStep is a target ordinal Step, independent of agent execution.
type RunStep struct {
	RunID     string
	Ordinal   int
	Kind      work.StepKind
	Iteration int
	Reason    string
	State     work.StepState
	StartedAt time.Time
	EndedAt   time.Time
	Result    json.RawMessage
}

// StartStepInput starts one idempotent target Step.
type StartStepInput struct {
	RunID     string
	Ordinal   int
	Kind      work.StepKind
	Iteration int
	Reason    string
	StartedAt time.Time
}

// StartStep persists one mandatory primary-operation boundary.
func (s *Store) StartStep(ctx context.Context, in StartStepInput) (RunStep, error) {
	runID, err := pgUUID(in.RunID)
	if err != nil {
		return RunStep{}, fmt.Errorf("starting step %d: %w", in.Ordinal, err)
	}
	row, err := s.q.StartTargetStep(ctx, storedb.StartTargetStepParams{RunID: runID, Ordinal: int32(in.Ordinal), Kind: string(in.Kind), Iteration: int32(in.Iteration), Reason: in.Reason, StartedAt: pgTimestamp(in.StartedAt)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			owned, ownershipErr := s.q.TargetRunOwned(ctx, runID)
			if ownershipErr != nil {
				return RunStep{}, fmt.Errorf("starting step %d of run %s: checking ownership: %w", in.Ordinal, in.RunID, wrapQueryErr(ownershipErr))
			}
			if !owned {
				return RunStep{}, fmt.Errorf("starting step %d of run %s: %w", in.Ordinal, in.RunID, ErrRunOwnership)
			}
			return RunStep{}, fmt.Errorf("starting step %d of run %s: conflicting retry: %w", in.Ordinal, in.RunID, work.ErrPermanent)
		}
		return RunStep{}, fmt.Errorf("starting step %d of run %s: %w", in.Ordinal, in.RunID, wrapQueryErr(err))
	}
	return runStepFromRow(row), nil
}

// CompleteStep persists a completed Step Result before the workflow chooses its next operation.
func (s *Store) CompleteStep(ctx context.Context, runID string, ordinal int, endedAt time.Time, result json.RawMessage) (RunStep, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return RunStep{}, fmt.Errorf("completing step %d: %w", ordinal, err)
	}
	row, err := s.q.CompleteTargetStep(ctx, storedb.CompleteTargetStepParams{RunID: id, Ordinal: int32(ordinal), EndedAt: pgTimestamp(endedAt), Result: result})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			owned, ownershipErr := s.q.TargetRunOwned(ctx, id)
			if ownershipErr != nil {
				return RunStep{}, fmt.Errorf("completing step %d of run %s: checking ownership: %w", ordinal, runID, wrapQueryErr(ownershipErr))
			}
			if !owned {
				return RunStep{}, fmt.Errorf("completing step %d of run %s: %w", ordinal, runID, ErrRunOwnership)
			}
			_, stepErr := s.q.TargetStep(ctx, storedb.TargetStepParams{RunID: id, Ordinal: int32(ordinal)})
			if errors.Is(stepErr, pgx.ErrNoRows) {
				return RunStep{}, fmt.Errorf("completing step %d of run %s: %w", ordinal, runID, ErrNotFound)
			}
			if stepErr != nil {
				return RunStep{}, fmt.Errorf("completing step %d of run %s: reading retry: %w", ordinal, runID, wrapQueryErr(stepErr))
			}
			return RunStep{}, fmt.Errorf("completing step %d of run %s: conflicting retry: %w", ordinal, runID, work.ErrPermanent)
		}
		return RunStep{}, fmt.Errorf("completing step %d of run %s: %w", ordinal, runID, wrapQueryErr(err))
	}
	return runStepFromRow(row), nil
}

// AgentAttempt is one workflow-authorized agent execution below a target Step.
type AgentAttempt struct {
	ID                TargetAttemptID
	AgentStage        work.AgentStage
	Model             work.Model
	State             work.AgentAttemptState
	FailureKind       work.RunFailureKind
	ExecutionID       string
	UsageState        work.UsageState
	Usage             work.Usage
	StartedAt         time.Time
	EndedAt           time.Time
	Result            json.RawMessage
	TranscriptPresent bool
}

// TargetAttemptID is the complete identity of one target Agent Attempt.
// Keeping it whole prevents a Run-scoped checkpoint capability from being
// paired with caller-selected Step or Attempt coordinates.
type TargetAttemptID struct {
	RunID       string
	StepOrdinal int
	AttemptNo   int
}

// String renders the stable compound identity for diagnostics and hashing.
func (id TargetAttemptID) String() string {
	return fmt.Sprintf("%s/step-%d/attempt-%d", id.RunID, id.StepOrdinal, id.AttemptNo)
}

// TargetStepDetail is a target Step with its agent executions in numeric order.
type TargetStepDetail struct {
	Step     RunStep
	Attempts []AgentAttempt
}

// TargetRunDetail is the target ordinal projection.
type TargetRunDetail struct {
	Run   Run
	Steps []TargetStepDetail
}

// TargetRunDetail reads one target Run's complete ordinal history without Temporal.
func (s *Store) TargetRunDetail(ctx context.Context, runID string) (TargetRunDetail, error) {
	id, err := pgUUID(runID)
	if err != nil {
		return TargetRunDetail{}, fmt.Errorf("reading target run detail: %w", err)
	}
	run, err := s.Run(ctx, runID)
	if err != nil {
		return TargetRunDetail{}, err
	}
	steps, err := s.q.TargetStepForRun(ctx, id)
	if err != nil {
		return TargetRunDetail{}, fmt.Errorf("reading target steps: %w", wrapQueryErr(err))
	}
	attempts, err := s.q.TargetAgentAttemptsForRun(ctx, id)
	if err != nil {
		return TargetRunDetail{}, fmt.Errorf("reading target agent attempts: %w", wrapQueryErr(err))
	}
	transcriptKeys, err := s.q.TargetTranscriptKeysForRun(ctx, id)
	if err != nil {
		return TargetRunDetail{}, fmt.Errorf("reading target transcript keys: %w", wrapQueryErr(err))
	}
	present := make(map[[2]int]bool, len(transcriptKeys))
	for _, key := range transcriptKeys {
		present[[2]int{int(key.StepOrdinal), int(key.AttemptNo)}] = true
	}
	byStep := make(map[int][]AgentAttempt, len(steps))
	for _, row := range attempts {
		attempt := agentAttemptFromRow(row)
		attempt.TranscriptPresent = present[[2]int{attempt.ID.StepOrdinal, attempt.ID.AttemptNo}]
		byStep[attempt.ID.StepOrdinal] = append(byStep[attempt.ID.StepOrdinal], attempt)
	}
	detail := TargetRunDetail{Run: run, Steps: make([]TargetStepDetail, 0, len(steps))}
	for _, row := range steps {
		step := runStepFromRow(row)
		detail.Steps = append(detail.Steps, TargetStepDetail{Step: step, Attempts: byStep[step.Ordinal]})
	}
	return detail, nil
}

// StartAgentAttemptInput authorizes one agent execution under a pre-existing Step.
type StartAgentAttemptInput struct {
	ID         TargetAttemptID
	AgentStage work.AgentStage
	Model      work.Model
	UsageState work.UsageState
	StartedAt  time.Time
}

// StartAgentAttempt persists an agent execution before its transcript can exist.
func (s *Store) StartAgentAttempt(ctx context.Context, in StartAgentAttemptInput) (AgentAttempt, error) {
	id, err := pgUUID(in.ID.RunID)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("starting agent attempt: %w", err)
	}
	row, err := s.q.StartTargetAgentAttempt(ctx, storedb.StartTargetAgentAttemptParams{RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo), AgentStage: string(in.AgentStage), Model: in.Model.Name, Effort: in.Model.Effort, UsageState: string(in.UsageState), StartedAt: pgTimestamp(in.StartedAt)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			owned, ownershipErr := s.q.TargetRunOwned(ctx, id)
			if ownershipErr != nil {
				return AgentAttempt{}, fmt.Errorf("starting agent attempt %s: checking ownership: %w", in.ID, wrapQueryErr(ownershipErr))
			}
			if !owned {
				return AgentAttempt{}, fmt.Errorf("starting agent attempt %s: %w", in.ID, ErrRunOwnership)
			}
			_, attemptErr := s.q.TargetAgentAttempt(ctx, storedb.TargetAgentAttemptParams{RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo)})
			if attemptErr == nil {
				return AgentAttempt{}, fmt.Errorf("starting agent attempt %s: conflicting retry: %w", in.ID, work.ErrPermanent)
			}
			if !errors.Is(attemptErr, pgx.ErrNoRows) {
				return AgentAttempt{}, fmt.Errorf("starting agent attempt %s: reading retry: %w", in.ID, wrapQueryErr(attemptErr))
			}
			return AgentAttempt{}, fmt.Errorf("starting agent attempt %s: %w", in.ID, ErrAgentAttemptStep)
		}
		return AgentAttempt{}, fmt.Errorf("starting agent attempt %s: %w", in.ID, wrapQueryErr(err))
	}
	return agentAttemptFromRow(row), nil
}

// AgentCheckpointInput is durable running progress or terminal evidence written by a scoped Run Worker capability.
type AgentCheckpointInput struct {
	ID          TargetAttemptID
	Capability  string
	ExecutionID string
	State       work.AgentAttemptState
	FailureKind work.RunFailureKind
	UsageState  work.UsageState
	Usage       work.Usage
	EndedAt     time.Time
	Result      json.RawMessage
	Transcript  *TargetTranscript
}

// AgentAttemptFailureInput records that the workflow exhausted one authorized
// execution without receiving a durable terminal response. It is deliberately
// main-control authority, unlike AgentCheckpointInput's scoped Run Worker
// capability: only the workflow may decide a fresh Agent Attempt is allowed.
type AgentAttemptFailureInput struct {
	ID          TargetAttemptID
	FailureKind work.RunFailureKind
	EndedAt     time.Time
}

// TargetTranscript is transcript material for one ordinal Agent Attempt.
type TargetTranscript struct {
	CompressedBytes       []byte
	Compression           string
	UncompressedSizeBytes int64
	Checksum              []byte
}

// GitCheckpoint is the durable recovery position for repository-affine work.
type GitCheckpoint struct {
	RunID             string
	StepOrdinal       int
	Branch            string
	PushedHead        string
	ObservedBase      string
	PullRequestNumber int
	PullRequestNodeID string
	StepResult        json.RawMessage
}

// GitCheckpointInput records a GitHub effect before its activity acknowledges success.
type GitCheckpointInput struct {
	GitCheckpoint
	CompletedAt time.Time
}

// CanceledRunRecovery contains the predecessor's only transferable state plus
// the exact outstanding Merge Step that can reconcile a lost merge response.
type CanceledRunRecovery struct {
	Checkpoint       GitCheckpoint
	MergeStepOrdinal int
}

// LatestCanceledRunCheckpoint finds the most recently canceled predecessor's
// durable pushed head. It deliberately does not return provider state or any
// credential material: a new Run gets only a Git object it can fetch itself.
func (s *Store) LatestCanceledRunCheckpoint(ctx context.Context, ticketID TicketID, excludingRunID string) (CanceledRunRecovery, bool, error) {
	excluding, err := pgUUID(excludingRunID)
	if err != nil {
		return CanceledRunRecovery{}, false, fmt.Errorf("reading canceled recovery checkpoint for ticket %d: %w", ticketID, err)
	}
	row, err := s.q.LatestCanceledRunGitCheckpoint(ctx, storedb.LatestCanceledRunGitCheckpointParams{
		TicketID: int64(ticketID),
		ID:       excluding,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CanceledRunRecovery{}, false, nil
	}
	if err != nil {
		return CanceledRunRecovery{}, false, fmt.Errorf("reading canceled recovery checkpoint for ticket %d: %w", ticketID, wrapQueryErr(err))
	}
	return CanceledRunRecovery{Checkpoint: GitCheckpoint{
		RunID:             runIDString(row.RunID),
		StepOrdinal:       int(row.StepOrdinal),
		Branch:            row.Branch,
		PushedHead:        row.PushedHead,
		ObservedBase:      row.ObservedBase,
		PullRequestNumber: int(row.PullRequestNumber),
		PullRequestNodeID: row.PullRequestNodeID,
		StepResult:        row.StepResult,
	}, MergeStepOrdinal: int(row.MergeStepOrdinal)}, true, nil
}

// RepositoryCheckpointInput is a repository Step checkpoint authorized by one
// exact active Run Worker generation. The capability is verified and discarded
// at the Store boundary; it is never persisted in clear text.
type RepositoryCheckpointInput struct {
	Identity      work.RunWorkerIdentity
	Capability    string
	GitCheckpoint GitCheckpoint
	CompletedAt   time.Time
}

// CheckpointGitEffect atomically persists the newest Git/PR recovery position
// and completes the corresponding repository-affine Step. It refuses an older
// position rather than allowing replacement recovery to regress a pushed head.
func (s *Store) CheckpointGitEffect(ctx context.Context, in GitCheckpointInput) (GitCheckpoint, error) {
	return s.checkpointGitEffect(ctx, in, nil, true)
}

type repositoryAuthorization struct {
	identity   work.RunWorkerIdentity
	capability string
}

func (s *Store) checkpointGitEffect(ctx context.Context, in GitCheckpointInput, authorization *repositoryAuthorization, completeStep bool) (GitCheckpoint, error) {
	if s.begin == nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: store cannot begin a transaction")
	}
	id, err := pgUUID(in.RunID)
	if err != nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: %w", err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	run, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		if authorization != nil && errors.Is(err, pgx.ErrNoRows) {
			return GitCheckpoint{}, fmt.Errorf("checkpointing repository effect: %w", ErrRunOwnership)
		}
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: reading run: %w", wrapQueryErr(err))
	}
	ticket, err := q.TargetTicketForUpdate(ctx, run.TicketID)
	if err != nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: reading ticket: %w", wrapQueryErr(err))
	}
	if run.TargetOutcome.Valid || ticket.State != TicketActive.String() || runIDString(ticket.ActiveRunID) != in.RunID {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: %w", ErrRunOwnership)
	}
	if authorization != nil {
		if authorization.identity.RunID != in.RunID {
			return GitCheckpoint{}, fmt.Errorf("checkpointing repository effect: identity names another Run: %w", ErrRunOwnership)
		}
		if err := authorizeRepositoryCapability(ctx, q, id, authorization.identity, authorization.capability); err != nil {
			return GitCheckpoint{}, fmt.Errorf("checkpointing repository effect: authorizing generation capability: %w", err)
		}
	}
	step, err := q.TargetStepForUpdate(ctx, storedb.TargetStepForUpdateParams{RunID: id, Ordinal: int32(in.StepOrdinal)})
	if err != nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: reading step: %w", wrapQueryErr(err))
	}
	previous, previousErr := q.TargetGitCheckpoint(ctx, id)
	if previousErr == nil {
		if previous.StepOrdinal > int32(in.StepOrdinal) || (previous.StepOrdinal == int32(in.StepOrdinal) && !gitCheckpointMatches(previous, in.GitCheckpoint)) {
			return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: older or conflicting checkpoint: %w", work.ErrPermanent)
		}
		if previous.StepOrdinal == int32(in.StepOrdinal) {
			if (completeStep && (step.State != string(work.StepStateCompleted) || !jsonEqual(step.Result, in.StepResult))) || (!completeStep && step.State != string(work.StepStateRunning)) {
				return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: conflicting completed step: %w", work.ErrPermanent)
			}
			if err := tx.Commit(ctx); err != nil {
				return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: committing retry: %w", wrapQueryErr(err))
			}
			return gitCheckpointFromRow(previous), nil
		}
	} else if !errors.Is(previousErr, pgx.ErrNoRows) {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: reading checkpoint: %w", wrapQueryErr(previousErr))
	}
	if step.State != string(work.StepStateRunning) {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: step is not running: %w", work.ErrPermanent)
	}
	row, err := q.PutTargetGitCheckpoint(ctx, storedb.PutTargetGitCheckpointParams{RunID: id, StepOrdinal: int32(in.StepOrdinal), Branch: in.Branch, PushedHead: in.PushedHead, ObservedBase: in.ObservedBase, PullRequestNumber: int32(in.PullRequestNumber), PullRequestNodeID: in.PullRequestNodeID, StepResult: in.StepResult})
	if err != nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: writing checkpoint: %w", wrapQueryErr(err))
	}
	if completeStep {
		if _, err := q.CompleteTargetStep(ctx, storedb.CompleteTargetStepParams{RunID: id, Ordinal: int32(in.StepOrdinal), EndedAt: pgTimestamp(in.CompletedAt), Result: in.StepResult}); err != nil {
			return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: completing step: %w", wrapQueryErr(err))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing git effect: committing: %w", wrapQueryErr(err))
	}
	return gitCheckpointFromRow(row), nil
}

// BindRepositoryCapability installs or monotonically rotates the capability
// for one active Run Worker generation. An exact retry is idempotent; an older
// generation or a different value for the same generation is rejected.
func (s *Store) BindRepositoryCapability(ctx context.Context, identity work.RunWorkerIdentity, capability string) error {
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("binding repository capability: %w", err)
	}
	if strings.TrimSpace(capability) == "" {
		return fmt.Errorf("binding repository capability: capability is empty: %w", work.ErrPermanent)
	}
	id, err := pgUUID(identity.RunID)
	if err != nil {
		return fmt.Errorf("binding repository capability: %w", err)
	}
	_, err = s.q.BindTargetRepositoryCapability(ctx, storedb.BindTargetRepositoryCapabilityParams{
		RunID: id, Generation: int64(identity.Generation), CapabilityHash: repositoryCapabilityHash(identity, capability),
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("binding repository capability for Run %s generation %d: %w", identity.RunID, identity.Generation, wrapQueryErr(err))
	}
	owned, ownershipErr := s.q.TargetRunOwned(ctx, id)
	if ownershipErr != nil {
		return fmt.Errorf("binding repository capability for Run %s generation %d: checking ownership: %w", identity.RunID, identity.Generation, wrapQueryErr(ownershipErr))
	}
	if !owned {
		return fmt.Errorf("binding repository capability for Run %s generation %d: %w", identity.RunID, identity.Generation, ErrRunOwnership)
	}
	return fmt.Errorf("binding repository capability for Run %s generation %d: conflicting or obsolete generation: %w", identity.RunID, identity.Generation, work.ErrPermanent)
}

// LoadRepositoryCheckpoint authenticates the current Run Worker generation
// before returning its durable Git/PR recovery position.
func (s *Store) LoadRepositoryCheckpoint(ctx context.Context, identity work.RunWorkerIdentity, capability string) (GitCheckpoint, bool, error) {
	if s.begin == nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: store cannot begin a transaction")
	}
	if err := identity.Validate(); err != nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: %w", err)
	}
	id, err := pgUUID(identity.RunID)
	if err != nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: %w", err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	run, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: %w", ErrRunOwnership)
		}
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: reading Run: %w", wrapQueryErr(err))
	}
	ticket, err := q.TargetTicketForUpdate(ctx, run.TicketID)
	if err != nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: reading Ticket: %w", wrapQueryErr(err))
	}
	if run.TargetOutcome.Valid || ticket.State != TicketActive.String() || runIDString(ticket.ActiveRunID) != identity.RunID {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: %w", ErrRunOwnership)
	}
	if err := authorizeRepositoryCapability(ctx, q, id, identity, capability); err != nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: authorizing generation capability: %w", err)
	}
	row, err := q.TargetGitCheckpoint(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: committing empty read: %w", wrapQueryErr(err))
		}
		return GitCheckpoint{}, false, nil
	}
	if err != nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: reading checkpoint: %w", wrapQueryErr(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: committing: %w", wrapQueryErr(err))
	}
	return gitCheckpointFromRow(row), true, nil
}

// CheckpointRepository verifies the generation capability and atomically
// persists the repository position plus its completed Step result.
func (s *Store) CheckpointRepository(ctx context.Context, in RepositoryCheckpointInput) (GitCheckpoint, error) {
	return s.checkpointRepository(ctx, in, true)
}

// CheckpointRepositoryEffect persists a recovery result without completing
// the target Step. Confirmed merge finalization owns that terminal transition.
func (s *Store) CheckpointRepositoryEffect(ctx context.Context, in RepositoryCheckpointInput) (GitCheckpoint, error) {
	return s.checkpointRepository(ctx, in, false)
}

func (s *Store) checkpointRepository(ctx context.Context, in RepositoryCheckpointInput, completeStep bool) (GitCheckpoint, error) {
	if err := in.Identity.Validate(); err != nil {
		return GitCheckpoint{}, fmt.Errorf("checkpointing repository effect: %w", err)
	}
	if strings.TrimSpace(in.Capability) == "" || in.GitCheckpoint.RunID != in.Identity.RunID {
		return GitCheckpoint{}, fmt.Errorf("checkpointing repository effect: identity or capability mismatch: %w", ErrRunOwnership)
	}
	return s.checkpointGitEffect(ctx, GitCheckpointInput{GitCheckpoint: in.GitCheckpoint, CompletedAt: in.CompletedAt}, &repositoryAuthorization{identity: in.Identity, capability: in.Capability}, completeStep)
}

func authorizeRepositoryCapability(ctx context.Context, q *storedb.Queries, runID pgtype.UUID, identity work.RunWorkerIdentity, capability string) error {
	row, err := q.TargetRepositoryCapabilityForUpdate(ctx, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("authorizing repository checkpoint: %w", ErrRunOwnership)
		}
		return fmt.Errorf("authorizing repository checkpoint: %w", wrapQueryErr(err))
	}
	if row.Generation != int64(identity.Generation) || row.CapabilityHash != repositoryCapabilityHash(identity, capability) {
		return fmt.Errorf("authorizing repository checkpoint: stale or foreign capability: %w", ErrRunOwnership)
	}
	return nil
}

// BindCheckpointCapability hashes and binds one capability to one exact active
// Agent Attempt. The clear capability is never persisted.
func (s *Store) BindCheckpointCapability(ctx context.Context, attemptID TargetAttemptID, capability string) error {
	id, err := pgUUID(attemptID.RunID)
	if err != nil {
		return fmt.Errorf("binding checkpoint capability: %w", err)
	}
	_, err = s.q.BindTargetAttemptCapability(ctx, storedb.BindTargetAttemptCapabilityParams{
		RunID: id, StepOrdinal: int32(attemptID.StepOrdinal), AttemptNo: int32(attemptID.AttemptNo), CheckpointCapabilityHash: pgOptionalText(capabilityHash(attemptID, capability)),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			owned, ownershipErr := s.q.TargetRunOwned(ctx, id)
			if ownershipErr != nil {
				return fmt.Errorf("binding checkpoint capability to %s: checking ownership: %w", attemptID, wrapQueryErr(ownershipErr))
			}
			if !owned {
				return fmt.Errorf("binding checkpoint capability to %s: %w", attemptID, ErrRunOwnership)
			}
			_, attemptErr := s.q.TargetAgentAttempt(ctx, storedb.TargetAgentAttemptParams{RunID: id, StepOrdinal: int32(attemptID.StepOrdinal), AttemptNo: int32(attemptID.AttemptNo)})
			if errors.Is(attemptErr, pgx.ErrNoRows) {
				return fmt.Errorf("binding checkpoint capability to %s: %w", attemptID, ErrNotFound)
			}
			if attemptErr != nil {
				return fmt.Errorf("binding checkpoint capability to %s: reading retry: %w", attemptID, wrapQueryErr(attemptErr))
			}
			return fmt.Errorf("binding checkpoint capability to %s: conflicting retry: %w", attemptID, work.ErrPermanent)
		}
		return fmt.Errorf("binding checkpoint capability to %s: %w", attemptID, wrapQueryErr(err))
	}
	return nil
}

// FailAgentAttempt records a terminal failure after Temporal has exhausted an
// authorized activity execution. The main workflow owns this decision, so it
// authenticates Run ownership rather than requiring the Run Worker's scoped
// checkpoint capability, which may be unavailable precisely when this path is
// needed.
func (s *Store) FailAgentAttempt(ctx context.Context, in AgentAttemptFailureInput) (AgentAttempt, error) {
	if s.begin == nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: store cannot begin a transaction")
	}
	if in.FailureKind == "" || in.EndedAt.IsZero() {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt %s: failure kind and terminal time are required: %w", in.ID, work.ErrPermanent)
	}
	id, err := pgUUID(in.ID.RunID)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: %w", err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	run, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: reading run: %w", wrapQueryErr(err))
	}
	ticket, err := q.TargetTicketForUpdate(ctx, run.TicketID)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: reading ticket: %w", wrapQueryErr(err))
	}
	if run.TargetOutcome.Valid || ticket.State != TicketActive.String() || runIDString(ticket.ActiveRunID) != in.ID.RunID {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: %w", ErrRunOwnership)
	}
	current, err := q.TargetAgentAttemptForUpdate(ctx, storedb.TargetAgentAttemptForUpdateParams{
		RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo),
	})
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: reading attempt: %w", wrapQueryErr(err))
	}
	if current.State != string(work.AgentAttemptRunning) {
		if current.State != string(work.AgentAttemptFailed) || current.FailureKind != string(in.FailureKind) || !timeFromPg(current.EndedAt).Equal(in.EndedAt.Truncate(time.Microsecond)) {
			return AgentAttempt{}, fmt.Errorf("failing agent attempt: conflicting terminal failure: %w", work.ErrPermanent)
		}
		if err := tx.Commit(ctx); err != nil {
			return AgentAttempt{}, fmt.Errorf("failing agent attempt: committing retry: %w", wrapQueryErr(err))
		}
		return agentAttemptFromRow(current), nil
	}
	row, err := q.CheckpointTargetAgentAttempt(ctx, storedb.CheckpointTargetAgentAttemptParams{
		RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo),
		ExecutionID: current.ExecutionID, State: string(work.AgentAttemptFailed), FailureKind: string(in.FailureKind),
		UsageState: current.UsageState, InputTokens: current.InputTokens, CachedInputTokens: current.CachedInputTokens,
		OutputTokens: current.OutputTokens, ReasoningTokens: current.ReasoningTokens, EndedAt: pgTimestamp(in.EndedAt),
	})
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt %s: %w", in.ID, wrapQueryErr(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentAttempt{}, fmt.Errorf("failing agent attempt: committing: %w", wrapQueryErr(err))
	}
	return agentAttemptFromRow(row), nil
}

// CheckpointAgentAttempt records only the named active Attempt after verifying the Run capability.
func (s *Store) CheckpointAgentAttempt(ctx context.Context, in AgentCheckpointInput) (AgentAttempt, error) {
	return s.checkpointAgentAttempt(ctx, in, true)
}

// FinalizeAgentWorkflowAttempt records immutable child-workflow evidence from
// the main-control activity. It is deliberately a separate API from the
// scoped Run Worker checkpoint door: an empty capability never authenticates
// the old direct-agent protocol.
func (s *Store) FinalizeAgentWorkflowAttempt(ctx context.Context, in AgentCheckpointInput) (AgentAttempt, error) {
	return s.checkpointAgentAttempt(ctx, in, false)
}

func (s *Store) checkpointAgentAttempt(ctx context.Context, in AgentCheckpointInput, requireCapability bool) (AgentAttempt, error) {
	if s.begin == nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: store cannot begin a transaction")
	}
	if err := in.Validate(); err != nil {
		return AgentAttempt{}, err
	}
	id, err := pgUUID(in.ID.RunID)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: %w", err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	run, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: reading run: %w", wrapQueryErr(err))
	}
	ticket, err := q.TargetTicketForUpdate(ctx, run.TicketID)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: reading ticket: %w", wrapQueryErr(err))
	}
	if run.TargetOutcome.Valid || ticket.State != TicketActive.String() || runIDString(ticket.ActiveRunID) != in.ID.RunID {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: %w", ErrRunOwnership)
	}
	current, err := q.TargetAgentAttemptForUpdate(ctx, storedb.TargetAgentAttemptForUpdateParams{
		RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo),
	})
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: reading attempt: %w", wrapQueryErr(err))
	}
	if requireCapability && (!current.CheckpointCapabilityHash.Valid || current.CheckpointCapabilityHash.String != capabilityHash(in.ID, in.Capability)) {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: %w", ErrRunOwnership)
	}
	if current.State != string(work.AgentAttemptRunning) {
		if !terminalAgentCheckpointMatches(current, in) {
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: conflicting terminal checkpoint: %w", work.ErrPermanent)
		}
		storedTranscript, transcriptErr := q.TargetAgentTranscript(ctx, storedb.TargetAgentTranscriptParams{RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo)})
		switch {
		case transcriptErr != nil && !errors.Is(transcriptErr, pgx.ErrNoRows):
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: reading terminal transcript: %w", wrapQueryErr(transcriptErr))
		case in.Transcript == nil:
		case errors.Is(transcriptErr, pgx.ErrNoRows):
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: conflicting terminal transcript: %w", work.ErrPermanent)
		case !targetTranscriptMatches(storedTranscript, *in.Transcript):
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: conflicting terminal transcript: %w", work.ErrPermanent)
		}
		if err := tx.Commit(ctx); err != nil {
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: committing retry: %w", wrapQueryErr(err))
		}
		return agentAttemptFromRow(current), nil
	}
	if in.State == work.AgentAttemptRunning && current.ExecutionID != "" {
		if !runningAgentCheckpointMatches(current, in) {
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: conflicting running checkpoint: %w", work.ErrPermanent)
		}
		storedTranscript, transcriptErr := q.TargetAgentTranscript(ctx, storedb.TargetAgentTranscriptParams{RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo)})
		switch {
		case transcriptErr != nil && !errors.Is(transcriptErr, pgx.ErrNoRows):
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: reading running transcript: %w", wrapQueryErr(transcriptErr))
		case in.Transcript == nil && errors.Is(transcriptErr, pgx.ErrNoRows):
		case in.Transcript == nil || errors.Is(transcriptErr, pgx.ErrNoRows):
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: conflicting running checkpoint: %w", work.ErrPermanent)
		case !targetTranscriptMatches(storedTranscript, *in.Transcript):
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: conflicting running checkpoint: %w", work.ErrPermanent)
		}
		if err := tx.Commit(ctx); err != nil {
			return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: committing retry: %w", wrapQueryErr(err))
		}
		return agentAttemptFromRow(current), nil
	}
	row, err := q.CheckpointTargetAgentAttempt(ctx, storedb.CheckpointTargetAgentAttemptParams{RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo), ExecutionID: in.ExecutionID, State: string(in.State), FailureKind: string(in.FailureKind), UsageState: string(in.UsageState), InputTokens: in.Usage.InputTokens, CachedInputTokens: in.Usage.CachedInputTokens, OutputTokens: in.Usage.OutputTokens, ReasoningTokens: in.Usage.ReasoningTokens, EndedAt: pgOptionalTimestamp(in.EndedAt), Result: in.Result})
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt %s: %w", in.ID, wrapQueryErr(err))
	}
	if in.Transcript != nil {
		err = q.PutTargetAgentTranscript(ctx, storedb.PutTargetAgentTranscriptParams{RunID: id, StepOrdinal: int32(in.ID.StepOrdinal), AttemptNo: int32(in.ID.AttemptNo), CompressedBytes: in.Transcript.CompressedBytes, Compression: in.Transcript.Compression, UncompressedSizeBytes: in.Transcript.UncompressedSizeBytes, Checksum: in.Transcript.Checksum})
		if err != nil {
			return AgentAttempt{}, fmt.Errorf("checkpointing transcript: %w", wrapQueryErr(err))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentAttempt{}, fmt.Errorf("checkpointing agent attempt: committing: %w", wrapQueryErr(err))
	}
	return agentAttemptFromRow(row), nil
}

// LoadAgentCheckpoint authenticates and reads one Attempt's durable provider
// evidence. found is false before the provider exposes its first thread ID.
func (s *Store) LoadAgentCheckpoint(ctx context.Context, attemptID TargetAttemptID, capability string) (AgentAttempt, *TargetTranscript, bool, error) {
	id, err := pgUUID(attemptID.RunID)
	if err != nil {
		return AgentAttempt{}, nil, false, fmt.Errorf("loading agent checkpoint: %w", err)
	}
	row, err := s.q.TargetAgentAttempt(ctx, storedb.TargetAgentAttemptParams{RunID: id, StepOrdinal: int32(attemptID.StepOrdinal), AttemptNo: int32(attemptID.AttemptNo)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentAttempt{}, nil, false, fmt.Errorf("loading agent checkpoint: %w", ErrRunOwnership)
		}
		return AgentAttempt{}, nil, false, fmt.Errorf("loading agent checkpoint: %w", wrapQueryErr(err))
	}
	if !row.CheckpointCapabilityHash.Valid || row.CheckpointCapabilityHash.String != capabilityHash(attemptID, capability) {
		return AgentAttempt{}, nil, false, fmt.Errorf("loading agent checkpoint: %w", ErrRunOwnership)
	}
	attempt := agentAttemptFromRow(row)
	if attempt.State == work.AgentAttemptRunning && attempt.ExecutionID == "" {
		return attempt, nil, false, nil
	}
	transcriptRow, transcriptErr := s.q.TargetAgentTranscript(ctx, storedb.TargetAgentTranscriptParams{RunID: id, StepOrdinal: int32(attemptID.StepOrdinal), AttemptNo: int32(attemptID.AttemptNo)})
	if transcriptErr != nil && !errors.Is(transcriptErr, pgx.ErrNoRows) {
		return AgentAttempt{}, nil, false, fmt.Errorf("loading agent checkpoint: reading transcript: %w", wrapQueryErr(transcriptErr))
	}
	var transcript *TargetTranscript
	if transcriptErr == nil {
		transcript = &TargetTranscript{CompressedBytes: transcriptRow.CompressedBytes, Compression: transcriptRow.Compression, UncompressedSizeBytes: transcriptRow.UncompressedSizeBytes, Checksum: transcriptRow.Checksum}
	}
	return attempt, transcript, true, nil
}

// TargetTranscript reads one transcript by ordinal Step identity. Unlike
// LoadAgentCheckpoint, this read-only console seam does not grant checkpoint
// authority and therefore accepts no capability.
func (s *Store) TargetTranscript(ctx context.Context, attemptID TargetAttemptID) (TargetTranscript, error) {
	runID, err := pgUUID(attemptID.RunID)
	if err != nil {
		return TargetTranscript{}, fmt.Errorf("reading target transcript: %w", err)
	}
	row, err := s.q.TargetAgentTranscript(ctx, storedb.TargetAgentTranscriptParams{RunID: runID, StepOrdinal: int32(attemptID.StepOrdinal), AttemptNo: int32(attemptID.AttemptNo)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TargetTranscript{}, fmt.Errorf("reading target transcript %s: %w", attemptID, ErrNotFound)
		}
		return TargetTranscript{}, fmt.Errorf("reading target transcript %s: %w", attemptID, wrapQueryErr(err))
	}
	return TargetTranscript{CompressedBytes: row.CompressedBytes, Compression: row.Compression, UncompressedSizeBytes: row.UncompressedSizeBytes, Checksum: row.Checksum}, nil
}

// ConfirmedMergeInput names the immutable merge evidence and its Merge Step.
type ConfirmedMergeInput struct {
	RunID        string
	TicketID     TicketID
	StepOrdinal  int
	ReviewedHead string
	MergeSHA     string
	EndedAt      time.Time
}

// TerminalResult is the durable outcome of terminal recording.
type TerminalResult struct {
	Ticket Ticket
	Run    Run
}

// FinalizeConfirmedMerge atomically completes the Merge Step, records immutable merge evidence,
// closes the Run, and releases dependency readiness by moving only its owned Ticket to done.
func (s *Store) FinalizeConfirmedMerge(ctx context.Context, in ConfirmedMergeInput) (TerminalResult, error) {
	if s.begin == nil {
		return TerminalResult{}, fmt.Errorf("finalizing merge: store cannot begin a transaction")
	}
	id, err := pgUUID(in.RunID)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("finalizing merge: %w", err)
	}
	for attempt := 0; attempt < confirmedMergeLockAttempts; attempt++ {
		result, attemptErr := s.finalizeConfirmedMergeAttempt(ctx, id, in)
		if !errors.Is(attemptErr, errConfirmedMergeOwnerChanged) {
			return result, attemptErr
		}
	}
	return TerminalResult{}, fmt.Errorf("finalizing merge: Ticket ownership kept changing: %w", ErrRunOwnership)
}

func (s *Store) finalizeConfirmedMergeAttempt(ctx context.Context, id pgtype.UUID, in ConfirmedMergeInput) (TerminalResult, error) {
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("finalizing merge: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	runRow, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("finalizing merge: reading run: %w", wrapQueryErr(err))
	}
	if runRow.TicketID != int64(in.TicketID) {
		return TerminalResult{}, fmt.Errorf("finalizing merge: %w", ErrRunOwnership)
	}
	if runRow.TargetOutcome.Valid {
		if runRow.TargetOutcome.String == string(work.RunOutcomeCanceled) {
			return reconcileConfirmedMergeAfterCancellation(ctx, tx, q, runRow, id, in)
		}
		if runRow.TargetOutcome.String != string(work.RunOutcomeSucceeded) || textFromPg(runRow.MergeSha) != in.MergeSHA || textFromPg(runRow.ReviewedHead) != in.ReviewedHead {
			return TerminalResult{}, fmt.Errorf("finalizing merge: conflicting terminal result: %w", work.ErrPermanent)
		}
		ticketRow, ticketErr := q.TargetTicketForUpdate(ctx, int64(in.TicketID))
		if ticketErr != nil {
			return TerminalResult{}, fmt.Errorf("finalizing merge retry: reading ticket: %w", wrapQueryErr(ticketErr))
		}
		ticket, parseErr := ticketFromRow(ticketRow)
		if parseErr != nil {
			return TerminalResult{}, parseErr
		}
		if err := tx.Commit(ctx); err != nil {
			return TerminalResult{}, fmt.Errorf("finalizing merge retry: committing: %w", wrapQueryErr(err))
		}
		return TerminalResult{Ticket: ticket, Run: runFromRow(runRow)}, nil
	}
	stepResult, err := confirmedMergeStepResult(in.MergeSHA)
	if err != nil {
		return TerminalResult{}, err
	}
	if _, err := q.CompleteTargetMergeStep(ctx, storedb.CompleteTargetMergeStepParams{RunID: id, Ordinal: int32(in.StepOrdinal), EndedAt: pgTimestamp(in.EndedAt), Result: stepResult}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TerminalResult{}, fmt.Errorf("finalizing merge: %w", ErrMergeStep)
		}
		return TerminalResult{}, fmt.Errorf("finalizing merge: completing step: %w", wrapQueryErr(err))
	}
	completedRun, err := q.CompleteTargetRunSuccess(ctx, storedb.CompleteTargetRunSuccessParams{ID: id, ReviewedHead: pgOptionalText(in.ReviewedHead), MergeSha: pgOptionalText(in.MergeSHA), EndedAt: pgTimestamp(in.EndedAt)})
	if err != nil {
		return TerminalResult{}, fmt.Errorf("finalizing merge: completing run: %w", wrapQueryErr(err))
	}
	ticketRow, err := q.CompleteTargetTicket(ctx, storedb.CompleteTargetTicketParams{ID: int64(in.TicketID), ActiveRunID: id})
	if err != nil {
		return TerminalResult{}, fmt.Errorf("finalizing merge: completing ticket: %w", ErrRunOwnership)
	}
	ticket, err := ticketFromRow(ticketRow)
	if err != nil {
		return TerminalResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TerminalResult{}, fmt.Errorf("finalizing merge: committing: %w", wrapQueryErr(err))
	}
	return TerminalResult{Ticket: ticket, Run: runFromRow(completedRun)}, nil
}

func reconcileConfirmedMergeAfterCancellation(
	ctx context.Context,
	tx pgx.Tx,
	q *storedb.Queries,
	runRow storedb.Run,
	runID pgtype.UUID,
	in ConfirmedMergeInput,
) (TerminalResult, error) {
	observedTicket, err := q.Ticket(ctx, runRow.TicketID)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: reading Ticket owner: %w", wrapQueryErr(err))
	}
	var successorRunID pgtype.UUID
	var successorLocked bool
	if observedTicket.State == TicketActive.String() && observedTicket.ActiveRunID.Valid {
		successorRunID = observedTicket.ActiveRunID
		successor, successorErr := q.TargetRunForUpdate(ctx, successorRunID)
		if successorErr != nil {
			return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: locking successor Run: %w", wrapQueryErr(successorErr))
		}
		if successor.TicketID != runRow.TicketID || successor.TargetOutcome.Valid {
			return TerminalResult{}, errConfirmedMergeOwnerChanged
		}
		successorLocked = true
	}
	ticketOwner, err := q.TargetTicketForUpdate(ctx, runRow.TicketID)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: locking Ticket owner: %w", wrapQueryErr(err))
	}
	switch {
	case ticketOwner.State == TicketOpen.String() && !ticketOwner.ActiveRunID.Valid && !successorLocked:
	case ticketOwner.State == TicketActive.String() && ticketOwner.ActiveRunID.Valid && successorLocked && ticketOwner.ActiveRunID == successorRunID:
		if _, err := q.CompleteTargetRunCanceled(ctx, storedb.CompleteTargetRunCanceledParams{ID: successorRunID, EndedAt: pgTimestamp(in.EndedAt)}); err != nil {
			return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: fencing successor Run: %w", wrapQueryErr(err))
		}
	default:
		if ticketOwner.State == TicketActive.String() && ticketOwner.ActiveRunID.Valid && !successorLocked {
			return TerminalResult{}, errConfirmedMergeOwnerChanged
		}
		return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: Ticket ownership changed: %w", ErrRunOwnership)
	}
	stepResult, err := confirmedMergeStepResult(in.MergeSHA)
	if err != nil {
		return TerminalResult{}, err
	}
	if in.StepOrdinal == 0 {
		recoveredStep, startErr := q.StartRecoveredTargetMergeStep(ctx, storedb.StartRecoveredTargetMergeStepParams{
			RunID: runID, StartedAt: pgTimestamp(in.EndedAt),
		})
		if startErr != nil {
			return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: starting recovery merge step: %w", wrapQueryErr(startErr))
		}
		in.StepOrdinal = int(recoveredStep.Ordinal)
	}
	if _, err := q.CompleteTargetMergeStep(ctx, storedb.CompleteTargetMergeStepParams{RunID: runID, Ordinal: int32(in.StepOrdinal), EndedAt: pgTimestamp(in.EndedAt), Result: stepResult}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: %w", ErrMergeStep)
		}
		return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: completing step: %w", wrapQueryErr(err))
	}
	completedRun, err := q.ReconcileCanceledTargetRunSuccess(ctx, storedb.ReconcileCanceledTargetRunSuccessParams{ID: runID, ReviewedHead: pgOptionalText(in.ReviewedHead), MergeSha: pgOptionalText(in.MergeSHA), EndedAt: pgTimestamp(in.EndedAt)})
	if err != nil {
		return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: completing canceled run: %w", wrapQueryErr(err))
	}
	var ticketRow storedb.Ticket
	if successorLocked {
		ticketRow, err = q.CompleteTargetTicket(ctx, storedb.CompleteTargetTicketParams{ID: runRow.TicketID, ActiveRunID: successorRunID})
	} else {
		ticketRow, err = q.CompleteCanceledTargetTicket(ctx, runRow.TicketID)
	}
	if err != nil {
		return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: completing Ticket: %w", ErrRunOwnership)
	}
	ticket, err := ticketFromRow(ticketRow)
	if err != nil {
		return TerminalResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TerminalResult{}, fmt.Errorf("reconciling confirmed merge: committing: %w", wrapQueryErr(err))
	}
	return TerminalResult{Ticket: ticket, Run: runFromRow(completedRun)}, nil
}

func confirmedMergeStepResult(mergeSHA string) (json.RawMessage, error) {
	result, err := json.Marshal(struct {
		Kind     string `json:"kind"`
		MergeSHA string `json:"merge_sha"`
	}{Kind: "merged", MergeSHA: mergeSHA})
	if err != nil {
		return nil, fmt.Errorf("encoding confirmed merge result: %w", err)
	}
	return result, nil
}

// CancelRunInput names one conditional cancellation finalization.
type CancelRunInput struct {
	RunID    string
	TicketID TicketID
	EndedAt  time.Time
}

// RunFailureInput names a workflow-owned terminal failure of an unmerged Run.
type RunFailureInput struct {
	RunID       string
	TicketID    TicketID
	Outcome     work.RunOutcome
	FailureKind work.RunFailureKind
	StepOrdinal int
	StepResult  json.RawMessage
	EndedAt     time.Time
}

// FinalizeRunFailure atomically records a specific failed Run and moves only
// its still-owned Ticket to failed. Existing cancellation or confirmed merge is
// authoritative and returned unchanged, so a late failure cannot reverse it.
func (s *Store) FinalizeRunFailure(ctx context.Context, in RunFailureInput) (TerminalResult, error) {
	if s.begin == nil {
		return TerminalResult{}, fmt.Errorf("failing run: store cannot begin a transaction")
	}
	id, err := pgUUID(in.RunID)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: %w", err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	runRow, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: reading run: %w", wrapQueryErr(err))
	}
	if runRow.TicketID != int64(in.TicketID) {
		return TerminalResult{}, fmt.Errorf("failing run: %w", ErrRunOwnership)
	}
	ticketRow, err := q.TargetTicketForUpdate(ctx, int64(in.TicketID))
	if err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: reading ticket: %w", wrapQueryErr(err))
	}
	if runRow.TargetOutcome.Valid {
		if (runRow.TargetOutcome.String != string(in.Outcome) || runRow.TargetFailureKind != string(in.FailureKind)) && runRow.TargetOutcome.String != string(work.RunOutcomeSucceeded) && runRow.TargetOutcome.String != string(work.RunOutcomeCanceled) {
			return TerminalResult{}, fmt.Errorf("failing run: conflicting terminal result: %w", work.ErrPermanent)
		}
		ticket, parseErr := ticketFromRow(ticketRow)
		if parseErr != nil {
			return TerminalResult{}, parseErr
		}
		if err := tx.Commit(ctx); err != nil {
			return TerminalResult{}, fmt.Errorf("failing run retry: committing: %w", wrapQueryErr(err))
		}
		return TerminalResult{Ticket: ticket, Run: runFromRow(runRow)}, nil
	}
	if ticketRow.State != TicketActive.String() || ticketRow.ActiveRunID != id {
		return TerminalResult{}, fmt.Errorf("failing run: %w", ErrRunOwnership)
	}
	if in.StepOrdinal > 0 {
		if _, err := q.FailRunningTargetAgentAttempts(ctx, storedb.FailRunningTargetAgentAttemptsParams{RunID: id, StepOrdinal: int32(in.StepOrdinal), FailureKind: string(in.FailureKind), EndedAt: pgTimestamp(in.EndedAt)}); err != nil {
			return TerminalResult{}, fmt.Errorf("failing run: failing active agent attempts: %w", wrapQueryErr(err))
		}
		if _, err := q.FailTargetStep(ctx, storedb.FailTargetStepParams{RunID: id, Ordinal: int32(in.StepOrdinal), EndedAt: pgTimestamp(in.EndedAt), Result: in.StepResult}); err != nil {
			return TerminalResult{}, fmt.Errorf("failing run: failing step: %w", wrapQueryErr(err))
		}
	}
	if in.Outcome != work.RunOutcomeFailed {
		return TerminalResult{}, fmt.Errorf("failing run: invalid terminal outcome: %w", work.ErrPermanent)
	}
	failedRun, err := q.CompleteTargetRunTerminal(ctx, storedb.CompleteTargetRunTerminalParams{ID: id, TargetOutcome: pgOptionalText(string(in.Outcome)), TargetFailureKind: string(in.FailureKind), EndedAt: pgTimestamp(in.EndedAt)})
	if err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: completing run: %w", wrapQueryErr(err))
	}
	failedTicket, err := q.FailTargetTicket(ctx, storedb.FailTargetTicketParams{ID: int64(in.TicketID), ActiveRunID: id})
	if err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: completing ticket: %w", ErrRunOwnership)
	}
	ticket, err := ticketFromRow(failedTicket)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: decoding completed ticket: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return TerminalResult{}, fmt.Errorf("failing run: committing: %w", wrapQueryErr(err))
	}
	return TerminalResult{Ticket: ticket, Run: runFromRow(failedRun)}, nil
}

// ReconcileAbandonedRun conditionally releases an active Ticket after direct
// workflow termination. It deliberately leaves the Run nonterminal: a
// maintainer observed abandoned ownership, not a normal cancellation result.
func (s *Store) ReconcileAbandonedRun(ctx context.Context, runID string, ticketID TicketID) (bool, error) {
	if s.begin == nil {
		return false, fmt.Errorf("reconciling abandoned run: store cannot begin a transaction")
	}
	id, err := pgUUID(runID)
	if err != nil {
		return false, fmt.Errorf("reconciling abandoned run: %w", err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("reconciling abandoned run: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	run, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reconciling abandoned run: reading run: %w", wrapQueryErr(err))
	}
	if run.TicketID != int64(ticketID) || run.TargetOutcome.Valid {
		if run.TargetOutcome.Valid {
			return false, fmt.Errorf("reconciling abandoned run: %w", ErrRunOwnership)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("reconciling abandoned run: committing no-op: %w", wrapQueryErr(err))
		}
		return false, nil
	}
	_, err = q.ReopenTargetTicket(ctx, storedb.ReopenTargetTicketParams{ID: int64(ticketID), ActiveRunID: id})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("reconciling abandoned run: reopening ticket: %w", wrapQueryErr(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("reconciling abandoned run: committing: %w", wrapQueryErr(err))
	}
	return err == nil, nil
}

// CancelRun closes an unmerged Run and reopens only the Ticket it still owns.
func (s *Store) CancelRun(ctx context.Context, in CancelRunInput) (TerminalResult, error) {
	if s.begin == nil {
		return TerminalResult{}, fmt.Errorf("canceling run: store cannot begin a transaction")
	}
	id, err := pgUUID(in.RunID)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("canceling run: %w", err)
	}
	tx, err := s.begin.Begin(ctx)
	if err != nil {
		return TerminalResult{}, fmt.Errorf("canceling run: beginning transaction: %w", wrapQueryErr(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	runRow, err := q.TargetRunForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TerminalResult{}, fmt.Errorf("canceling run: %w", ErrNoOwnedClaim)
		}
		return TerminalResult{}, fmt.Errorf("canceling run: reading run: %w", wrapQueryErr(err))
	}
	if runRow.TicketID != int64(in.TicketID) {
		return TerminalResult{}, fmt.Errorf("canceling run: %w", ErrRunOwnership)
	}
	if runRow.TargetOutcome.Valid && runRow.TargetOutcome.String == string(work.RunOutcomeCanceled) {
		ticketRow, ticketErr := q.TargetTicketForUpdate(ctx, int64(in.TicketID))
		if ticketErr != nil {
			return TerminalResult{}, fmt.Errorf("canceling run retry: reading ticket: %w", wrapQueryErr(ticketErr))
		}
		if ticketRow.State != TicketOpen.String() || ticketRow.ActiveRunID.Valid {
			return TerminalResult{}, fmt.Errorf("canceling run retry: %w", ErrRunOwnership)
		}
		ticket, parseErr := ticketFromRow(ticketRow)
		if parseErr != nil {
			return TerminalResult{}, parseErr
		}
		if err := tx.Commit(ctx); err != nil {
			return TerminalResult{}, fmt.Errorf("canceling run retry: committing: %w", wrapQueryErr(err))
		}
		return TerminalResult{Ticket: ticket, Run: runFromRow(runRow)}, nil
	}
	if runRow.TargetOutcome.Valid && runRow.TargetOutcome.String != string(work.RunOutcomeCanceled) {
		ticketRow, ticketErr := q.TargetTicketForUpdate(ctx, int64(in.TicketID))
		if ticketErr != nil {
			return TerminalResult{}, fmt.Errorf("canceling completed run: reading ticket: %w", wrapQueryErr(ticketErr))
		}
		ticket, parseErr := ticketFromRow(ticketRow)
		if parseErr != nil {
			return TerminalResult{}, parseErr
		}
		if err := tx.Commit(ctx); err != nil {
			return TerminalResult{}, fmt.Errorf("canceling completed run: committing: %w", wrapQueryErr(err))
		}
		return TerminalResult{Ticket: ticket, Run: runFromRow(runRow)}, nil
	}
	ticketOwner, err := q.TargetTicketForUpdate(ctx, int64(in.TicketID))
	if err != nil {
		return TerminalResult{}, fmt.Errorf("canceling run: reading ticket owner: %w", wrapQueryErr(err))
	}
	if ticketOwner.State != TicketActive.String() || ticketOwner.ActiveRunID != id {
		return TerminalResult{}, fmt.Errorf("canceling run: %w", ErrRunOwnership)
	}
	runRow, err = q.CompleteTargetRunCanceled(ctx, storedb.CompleteTargetRunCanceledParams{ID: id, EndedAt: pgTimestamp(in.EndedAt)})
	if err != nil {
		return TerminalResult{}, fmt.Errorf("canceling run: completing: %w", wrapQueryErr(err))
	}
	ticketRow, err := q.ReopenTargetTicket(ctx, storedb.ReopenTargetTicketParams{ID: int64(in.TicketID), ActiveRunID: id})
	if err != nil {
		// A retry after the ticket was reopened is still a successful canceled outcome.
		ticketRow, err = q.TargetTicketForUpdate(ctx, int64(in.TicketID))
		if err != nil {
			return TerminalResult{}, fmt.Errorf("canceling run: reading ticket: %w", wrapQueryErr(err))
		}
	}
	ticket, err := ticketFromRow(ticketRow)
	if err != nil {
		return TerminalResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TerminalResult{}, fmt.Errorf("canceling run: committing: %w", wrapQueryErr(err))
	}
	return TerminalResult{Ticket: ticket, Run: runFromRow(runRow)}, nil
}

// Validate reports whether a checkpoint contains the durable evidence its state requires.
func (in AgentCheckpointInput) Validate() error {
	if in.Transcript != nil && (in.Transcript.UncompressedSizeBytes > work.MaxTargetTranscriptUncompressedBytes || len(in.Transcript.CompressedBytes) > work.MaxTargetTranscriptCompressedBytes) {
		return fmt.Errorf("checkpointing agent attempt %s: transcript exceeds durable size limit: %w", in.ID, work.ErrPermanent)
	}
	if in.State != work.AgentAttemptRunning && in.State != work.AgentAttemptSucceeded && in.State != work.AgentAttemptFailed {
		return fmt.Errorf("checkpointing agent attempt %s: invalid state: %w", in.ID, work.ErrPermanent)
	}
	if in.UsageState != work.UsageUnknown && in.UsageState != work.UsageMeasured {
		return fmt.Errorf("checkpointing agent attempt %s: usage state is required: %w", in.ID, work.ErrPermanent)
	}
	if in.State == work.AgentAttemptRunning {
		if in.ExecutionID == "" {
			return fmt.Errorf("checkpointing agent attempt %s: execution identity is required: %w", in.ID, work.ErrPermanent)
		}
		if !in.EndedAt.IsZero() || len(in.Result) != 0 {
			return fmt.Errorf("checkpointing agent attempt %s: running checkpoint cannot be terminal: %w", in.ID, work.ErrPermanent)
		}
		return nil
	}
	if in.EndedAt.IsZero() {
		return fmt.Errorf("checkpointing agent attempt %s: terminal time is required: %w", in.ID, work.ErrPermanent)
	}
	if in.State != work.AgentAttemptSucceeded {
		return nil
	}
	if in.ExecutionID == "" {
		return fmt.Errorf("checkpointing agent attempt %s: execution identity is required: %w", in.ID, work.ErrPermanent)
	}
	if len(in.Result) == 0 || !json.Valid(in.Result) {
		return fmt.Errorf("checkpointing agent attempt %s: terminal result is required: %w", in.ID, work.ErrPermanent)
	}
	if in.Transcript == nil || len(in.Transcript.CompressedBytes) == 0 || in.Transcript.Compression == "" || len(in.Transcript.Checksum) == 0 {
		return fmt.Errorf("checkpointing agent attempt %s: transcript is required: %w", in.ID, work.ErrPermanent)
	}
	return nil
}

func capabilityHash(attemptID TargetAttemptID, capability string) string {
	material := attemptID.String() + "\x00" + capability
	return fmt.Sprintf("%x", sha256.Sum256([]byte(material)))
}

func repositoryCapabilityHash(identity work.RunWorkerIdentity, capability string) string {
	material := fmt.Sprintf("%s/generation-%d\x00%s", identity.RunID, identity.Generation, capability)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(material)))
}

func terminalAgentCheckpointMatches(current storedb.RunAgentAttempt, in AgentCheckpointInput) bool {
	return current.State == string(in.State) &&
		current.ExecutionID == in.ExecutionID &&
		current.FailureKind == string(in.FailureKind) &&
		current.UsageState == string(in.UsageState) &&
		current.InputTokens == in.Usage.InputTokens &&
		current.CachedInputTokens == in.Usage.CachedInputTokens &&
		current.OutputTokens == in.Usage.OutputTokens &&
		current.ReasoningTokens == in.Usage.ReasoningTokens &&
		timeFromPg(current.EndedAt).Equal(in.EndedAt.Truncate(time.Microsecond)) &&
		jsonEqual(current.Result, in.Result)
}

func runningAgentCheckpointMatches(current storedb.RunAgentAttempt, in AgentCheckpointInput) bool {
	return terminalAgentCheckpointMatches(current, in) && in.State == work.AgentAttemptRunning && in.EndedAt.IsZero() && len(in.Result) == 0
}

func targetTranscriptMatches(current storedb.RunAgentTranscript, in TargetTranscript) bool {
	return bytes.Equal(current.CompressedBytes, in.CompressedBytes) &&
		current.Compression == in.Compression &&
		current.UncompressedSizeBytes == in.UncompressedSizeBytes &&
		bytes.Equal(current.Checksum, in.Checksum)
}

func gitCheckpointMatches(current storedb.RunGitCheckpoint, in GitCheckpoint) bool {
	return current.StepOrdinal == int32(in.StepOrdinal) &&
		current.Branch == in.Branch &&
		current.PushedHead == in.PushedHead &&
		current.ObservedBase == in.ObservedBase &&
		current.PullRequestNumber == int32(in.PullRequestNumber) &&
		current.PullRequestNodeID == in.PullRequestNodeID &&
		jsonEqual(current.StepResult, in.StepResult)
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

func runStepFromRow(row storedb.RunStep) RunStep {
	return RunStep{RunID: runIDString(row.RunID), Ordinal: int(row.Ordinal), Kind: work.StepKind(row.Kind), Iteration: int(row.Iteration), Reason: row.Reason, State: work.StepState(row.State), StartedAt: timeFromPg(row.StartedAt), EndedAt: timeFromPg(row.EndedAt), Result: row.Result}
}

func agentAttemptFromRow(row storedb.RunAgentAttempt) AgentAttempt {
	return AgentAttempt{ID: TargetAttemptID{RunID: runIDString(row.RunID), StepOrdinal: int(row.StepOrdinal), AttemptNo: int(row.AttemptNo)}, AgentStage: work.AgentStage(row.AgentStage), Model: work.Model{Name: row.Model, Effort: row.Effort}, State: work.AgentAttemptState(row.State), FailureKind: work.RunFailureKind(row.FailureKind), ExecutionID: row.ExecutionID, UsageState: work.UsageState(row.UsageState), Usage: work.Usage{InputTokens: row.InputTokens, CachedInputTokens: row.CachedInputTokens, OutputTokens: row.OutputTokens, ReasoningTokens: row.ReasoningTokens}, StartedAt: timeFromPg(row.StartedAt), EndedAt: timeFromPg(row.EndedAt), Result: row.Result}
}

func gitCheckpointFromRow(row storedb.RunGitCheckpoint) GitCheckpoint {
	return GitCheckpoint{RunID: runIDString(row.RunID), StepOrdinal: int(row.StepOrdinal), Branch: row.Branch, PushedHead: row.PushedHead, ObservedBase: row.ObservedBase, PullRequestNumber: int(row.PullRequestNumber), PullRequestNodeID: row.PullRequestNodeID, StepResult: row.StepResult}
}

var (
	_ TargetRunClaimer       = (*Store)(nil)
	_ TargetStepRecorder     = (*Store)(nil)
	_ TargetAgentRecorder    = (*Store)(nil)
	_ TargetTerminalRecorder = (*Store)(nil)
)
