package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const credentialRenewalInterval = 30 * time.Minute

const (
	targetUnboundedChangeID    = "work-on-ticket-unbounded-v1"
	targetUnboundedVersion     = 1
	legacyTargetMaxReviewSteps = 5
)

// targetRecordingActs, runWorkerControlActs, and targetRunWorkerActs name the
// target activity boundaries whose method names are already unique. Temporal
// resolves their registered method names; workflow code never invokes the nil
// receivers directly.
var (
	targetRecordingActs  *activities.TargetRecordingActivities
	targetRecoveryActs   *activities.TargetRecoveryActivities
	runWorkerControlActs *activities.RunWorkerControlActivities
	targetRunWorkerActs  *activities.RunWorkerActivities
)

// WorkOnTicketInput is the immutable admission policy and repository source
// for one target Ticket workflow.
type WorkOnTicketInput struct {
	TicketID store.TicketID
	// RunID is retained for checked-in replay fixtures and focused workflow
	// tests. Production admission leaves it empty so WorkOnTicket binds the
	// durable Run to its authoritative Temporal execution Run ID.
	RunID    string
	Policy   work.TargetRunPolicy
	CloneURL string
	Model    work.Model
}

type semanticDeadlineContextKey struct{}

type targetLegacyPayloadContextKey struct{}

type targetStepTrackerContextKey struct{}

type targetStepTracker struct {
	ordinal int
	kind    work.StepKind
	active  bool
}

type terminalRunError struct {
	err         error
	outcome     work.RunOutcome
	failureKind work.RunFailureKind
	stepOrdinal int
	stepResult  json.RawMessage
}

// targetAgentStepResult is the parent-owned summary of one completed child
// AgentWorkflow. It deliberately does not reuse the retired direct-provider
// activity output: conversation and transcript references are opaque,
// attempt-owned workflow evidence.
type targetAgentStepResult struct {
	Key             work.StageKey
	Identity        string
	Raw             json.RawMessage
	Result          work.StageOutput
	Usage           work.Usage
	UsageMeasured   bool
	ConversationRef agent.ConversationRef
	TranscriptRef   agent.TranscriptRef
}

func (e *terminalRunError) Error() string { return e.err.Error() }

func (e *terminalRunError) Unwrap() error { return e.err }

// WorkOnTicket claims one Ticket before creating generation one, creates its
// private Run Worker Session, and clones the repository as that Session's
// first repository-affine activity.
func WorkOnTicket(ctx workflow.Context, in WorkOnTicketInput) (runErr error) {
	if in.RunID == "" {
		in.RunID = workflow.GetInfo(ctx).WorkflowExecution.RunID
	}
	var claimed store.ClaimRunResult
	var session *targetRunSession
	claimedRun := false
	claimStarted := false
	mergeConfirmed := false
	terminalFinalized := false
	var trackedStep *targetStepTracker
	defer func() {
		if !claimedRun {
			if claimStarted && temporal.IsCanceledError(runErr) {
				cleanupCtx, cancel := workflow.NewDisconnectedContext(ctx)
				defer cancel()
				finalCtx := workflow.WithActivityOptions(cleanupCtx, targetActivityOptions(in.Policy.Recording))
				if err := workflow.ExecuteActivity(finalCtx, targetRecordingActs.CancelRunIfClaimed, store.CancelRunInput{
					RunID: in.RunID, TicketID: in.TicketID, EndedAt: workflow.Now(cleanupCtx),
				}).Get(finalCtx, nil); err != nil {
					runErr = fmt.Errorf("reconciling canceled target claim: %w", err)
				}
			}
			return
		}
		// Once GitHub has confirmed either this Run's merge or its canceled
		// predecessor's merge, a persistence failure must never rewrite that
		// irreversible external fact as a failed or canceled Run.
		if mergeConfirmed {
			if runErr != nil && session != nil {
				cleanupCtx, cancel := workflow.NewDisconnectedContext(ctx)
				defer cancel()
				session.close()
				session.reportTerminalDelete(cleanupCtx, "confirmed merge reconciliation cleanup")
			}
			return
		}
		if terminalFinalized {
			return
		}
		var terminal *terminalRunError
		_ = errors.As(runErr, &terminal)
		if outcome, failureKind, failed := terminalFailureKind(runErr); failed {
			terminalCtx, cancel := workflow.NewDisconnectedContext(ctx)
			defer cancel()
			finalCtx := workflow.WithActivityOptions(terminalCtx, targetActivityOptions(in.Policy.Recording))
			failureInput := store.RunFailureInput{RunID: in.RunID, TicketID: in.TicketID, Outcome: outcome, FailureKind: failureKind, EndedAt: workflow.Now(terminalCtx)}
			if terminal != nil {
				failureInput.StepOrdinal = terminal.stepOrdinal
				failureInput.StepResult = terminal.stepResult
			} else if trackedStep != nil && trackedStep.active {
				result, err := json.Marshal(struct {
					Kind        string              `json:"kind"`
					StepKind    work.StepKind       `json:"step_kind"`
					FailureKind work.RunFailureKind `json:"failure_kind"`
				}{Kind: "terminal_failure", StepKind: trackedStep.kind, FailureKind: failureKind})
				if err != nil {
					runErr = fmt.Errorf("encoding terminal result for step %d: %w", trackedStep.ordinal, err)
					return
				}
				failureInput.StepOrdinal = trackedStep.ordinal
				failureInput.StepResult = result
			}
			if err := workflow.ExecuteActivity(finalCtx, targetRecordingActs.FinalizeRunFailure, failureInput).Get(finalCtx, nil); err != nil {
				runErr = fmt.Errorf("recording failed target run: %w", err)
				return
			}
			if terminal != nil {
				runErr = terminal.err
			}
			if session != nil {
				session.close()
				session.reportTerminalDelete(terminalCtx, "failed run cleanup")
			}
			return
		}
		if !temporal.IsCanceledError(runErr) {
			return
		}
		cleanupCtx, cancel := workflow.NewDisconnectedContext(ctx)
		defer cancel()
		finalCtx := workflow.WithActivityOptions(cleanupCtx, targetActivityOptions(in.Policy.Recording))
		if err := workflow.ExecuteActivity(finalCtx, targetRecordingActs.CancelRun, store.CancelRunInput{
			RunID: in.RunID, TicketID: in.TicketID, EndedAt: workflow.Now(cleanupCtx),
		}).Get(finalCtx, nil); err != nil {
			runErr = fmt.Errorf("recording canceled target run: %w", err)
			return
		}
		if session != nil {
			session.close()
			session.reportTerminalDelete(cleanupCtx, "canceled run cleanup")
		}
	}()
	if err := validateWorkOnTicket(in); err != nil {
		return fmt.Errorf("validating WorkOnTicket input: %w", err)
	}
	hardDeadline := workflow.Now(ctx).Add(in.Policy.HardDeadline)
	claimStarted = true
	claimCtx := workflow.WithActivityOptions(ctx, targetActivityOptions(in.Policy.Recording))
	if err := workflow.ExecuteActivity(claimCtx, targetRecordingActs.ClaimAndStartRun, store.ClaimRunInput{
		TicketID:  in.TicketID,
		RunID:     in.RunID,
		StartedAt: workflow.Now(ctx),
	}).Get(claimCtx, &claimed); err != nil {
		return fmt.Errorf("claiming ticket %d: %w", in.TicketID, err)
	}
	claimedRun = true
	ctx = workflow.WithValue(ctx, semanticDeadlineContextKey{}, workflow.Now(ctx).Add(in.Policy.SemanticDeadline))
	trackedStep = &targetStepTracker{}
	ctx = workflow.WithValue(ctx, targetStepTrackerContextKey{}, trackedStep)
	var recovery activities.CanceledRunCheckpoint
	if err := workflow.ExecuteActivity(claimCtx, targetRecoveryActs.LatestCanceledRunCheckpoint, claimed.Ticket.ID, in.RunID).Get(claimCtx, &recovery); err != nil {
		return fmt.Errorf("reading canceled run recovery checkpoint: %w", err)
	}

	branch := work.FactoryTicketBranchName(int64(claimed.Ticket.ID), in.RunID)
	session, err := newTargetRunSession(ctx, in, int(claimed.Ticket.ID), branch, hardDeadline)
	if err != nil {
		return fmt.Errorf("preparing the initial Run Worker Session: %w", err)
	}
	if err := startTargetStep(ctx, in, 3, work.StepCloneRepository); err != nil {
		return fmt.Errorf("starting repository clone: %w", err)
	}
	var clone activities.CloneTargetRepositoryOutput
	if err := session.execute(ctx, func(sessionCtx workflow.Context) error {
		return workflow.ExecuteActivity(sessionCtx, targetRunWorkerActs.CloneTargetRepository, activities.CloneTargetRepositoryInput{
			Step:                    activities.RepositoryStep{StepOrdinal: 3, Branch: branch},
			CloneURL:                in.CloneURL,
			CarryForwardHead:        carryForwardHead(recovery),
			RetirePullRequestNumber: canceledRunPullRequestNumber(recovery),
		}).Get(sessionCtx, &clone)
	}); err != nil {
		return fmt.Errorf("cloning the target repository: %w", err)
	}
	completeTrackedStep(ctx, 3)
	if clone.PredecessorMerge != nil {
		if !recovery.Found {
			return fmt.Errorf("reconciling canceled predecessor merge without recovery checkpoint: %w", work.ErrPermanent)
		}
		mergeConfirmed = true
		terminalCtx, cancel := workflow.NewDisconnectedContext(ctx)
		defer cancel()
		finalCtx := workflow.WithActivityOptions(terminalCtx, targetActivityOptions(in.Policy.Recording))
		if err := workflow.ExecuteActivity(finalCtx, targetRecordingActs.FinalizeConfirmedMerge, store.ConfirmedMergeInput{
			RunID: recovery.Checkpoint.RunID, TicketID: in.TicketID, StepOrdinal: recovery.MergeStepOrdinal,
			ReviewedHead: clone.PredecessorMerge.ReviewedHead, MergeSHA: clone.PredecessorMerge.MergeSHA, EndedAt: workflow.Now(terminalCtx),
		}).Get(finalCtx, nil); err != nil {
			return fmt.Errorf("recording confirmed predecessor merge: %w", err)
		}
		session.close()
		session.reportTerminalDelete(terminalCtx, "fenced successor cleanup")
		return temporal.NewNonRetryableApplicationError(
			"confirmed predecessor merge fenced this successor Run",
			activities.ErrTypePredecessorMergeFenced,
			nil,
		)
	}
	session.checkoutReady = true

	legacyPayload := workflow.GetVersion(ctx, targetUnboundedChangeID, workflow.DefaultVersion, targetUnboundedVersion) == workflow.DefaultVersion
	ctx = workflow.WithValue(ctx, targetLegacyPayloadContextKey{}, legacyPayload)
	detail := work.TicketDetail{Ticket: work.Ticket{Number: int(claimed.Ticket.ID), Title: claimed.Ticket.Title, Body: claimed.Ticket.Body}}
	ordinal, reviewSteps := 4, 0
	plan, _, err := runTargetAgentStep(ctx, session, in, detail, ordinal, work.AgentStagePlan, work.PriorTurns{}, work.AgentPromptContext{}, 1, nil)
	if err != nil {
		return fmt.Errorf("running target plan: %w", err)
	}
	ordinal++
	implement, _, err := runTargetAgentStep(ctx, session, in, detail, ordinal, work.AgentStageImplement, work.PriorTurns{Plan: plan.Result}, work.AgentPromptContext{}, 1, nil)
	if err != nil {
		return fmt.Errorf("running initial target implementation: %w", err)
	}
	implementSeed := targetImplementSeed(implement)
	ordinal++
	implementTurn := 1
	var pullRequest work.PullRequest
	var mergeStep activities.RepositoryStep
	var merge work.PullRequestMergeResult
	var replacementCandidate *work.PullRequest
	ready := false
	var feedback work.AgentPromptContext
	var latestReview work.StageOutput
	var reviewLedger []work.ReviewTurnRecord

	for {
		if replacementCandidate != nil {
			pullRequest = *replacementCandidate
			replacementCandidate = nil
		} else {
			syncStep := activities.RepositoryStep{StepOrdinal: ordinal, Branch: branch, PushedHead: clone.HeadSHA}
			if err := startTargetStep(ctx, in, syncStep.StepOrdinal, work.StepSyncPullRequest); err != nil {
				return fmt.Errorf("starting target pull request synchronization: %w", err)
			}
			ordinal++
			if err := session.execute(ctx, func(sessionCtx workflow.Context) error {
				return workflow.ExecuteActivity(sessionCtx, targetRunWorkerActs.TargetSyncPullRequest, activities.TargetSyncPullRequestInput{
					Step: syncStep, Title: implementTitle(implement.Result), Body: implementBody(implement.Result), Existing: optionalPullRequest(pullRequest),
				}).Get(sessionCtx, &pullRequest)
			}); err != nil {
				return fmt.Errorf("synchronizing target pull request: %w", err)
			}
			completeTrackedStep(ctx, syncStep.StepOrdinal)
		}
		if strings.TrimSpace(pullRequest.HeadSHA) == "" || pullRequest.Number <= 0 || strings.TrimSpace(pullRequest.NodeID) == "" {
			return temporal.NewNonRetryableApplicationError("target pull request does not identify an authoritative candidate head", activities.ErrTypeInvalid, nil)
		}

		candidate := activities.RepositoryStep{StepOrdinal: ordinal, Branch: branch, PushedHead: pullRequest.HeadSHA, PullRequestNumber: pullRequest.Number, PullRequestNodeID: pullRequest.NodeID}
		if err := startTargetStep(ctx, in, candidate.StepOrdinal, work.StepAwaitCI); err != nil {
			return fmt.Errorf("starting target CI observation: %w", err)
		}
		ordinal++
		var ci activities.AwaitCIOutput
		if err := session.execute(ctx, func(sessionCtx workflow.Context) error {
			ciCtx := workflow.WithActivityOptions(sessionCtx, targetActivityOptions(in.Policy.AwaitCI))
			return workflow.ExecuteActivity(ciCtx, targetRunWorkerActs.TargetAwaitCI, activities.TargetAwaitCIInput{Step: candidate, CI: activities.AwaitCIInput{CommitSHA: candidate.PushedHead, RequiredChecks: in.Policy.RequiredChecks}}).Get(ciCtx, &ci)
		}); err != nil {
			if isScheduleToCloseTimeout(err) || !semanticTimeRemaining(ctx) {
				return finalizeUnobservedCI(ctx, session, in, candidate.StepOrdinal)
			}
			return fmt.Errorf("awaiting target CI for %s: %w", candidate.PushedHead, err)
		}
		completeTrackedStep(ctx, candidate.StepOrdinal)
		if ci.CommitSHA != candidate.PushedHead {
			return temporal.NewNonRetryableApplicationError(fmt.Sprintf("target CI returned another candidate %q", ci.CommitSHA), activities.ErrTypeInvalid, nil)
		}
		if !ci.Green {
			implementTurn++
			feedback = work.AgentPromptContext{CandidateHeadSHA: candidate.PushedHead, CIFailures: ci.RedFailures}
			implement, _, err = runTargetAgentStep(ctx, session, in, detail, ordinal, work.AgentStageImplement, work.PriorTurns{Plan: plan.Result, LatestImplement: implement.Result}, feedback, implementTurn, implementSeed)
			if err != nil {
				return fmt.Errorf("running target implementation after CI feedback: %w", err)
			}
			implementSeed = targetImplementSeed(implement)
			ordinal++
			continue
		}
		reviewSteps++
		review, _, reviewErr := runTargetAgentStep(ctx, session, in, detail, ordinal, work.AgentStageReview, work.PriorTurns{Plan: plan.Result, LatestImplement: implement.Result, LatestReview: latestReview, ReviewLedger: reviewLedger}, work.AgentPromptContext{CandidateHeadSHA: candidate.PushedHead}, reviewSteps, nil)
		if reviewErr != nil {
			return fmt.Errorf("running target review %d: %w", reviewSteps, reviewErr)
		}
		ordinal++
		findings, ok := review.Result.Value().(work.ReviewOutput)
		if !ok {
			return temporal.NewNonRetryableApplicationError("target review produced an invalid result", activities.ErrTypeInvalid, nil)
		}
		latestReview = review.Result
		reviewLedger = append(reviewLedger, work.ReviewTurnRecord{Turn: reviewSteps, Findings: findings.Findings, Verified: findings.Verified})
		if len(findings.BlockingFindingIDs()) != 0 {
			implementTurn++
			feedback = work.AgentPromptContext{CandidateHeadSHA: candidate.PushedHead, ReviewFindings: findings.Findings}
			implement, _, err = runTargetAgentStep(ctx, session, in, detail, ordinal, work.AgentStageImplement, work.PriorTurns{Plan: plan.Result, LatestImplement: implement.Result, LatestReview: latestReview}, feedback, implementTurn, implementSeed)
			if err != nil {
				return fmt.Errorf("running target implementation after review feedback: %w", err)
			}
			implementSeed = targetImplementSeed(implement)
			ordinal++
			continue
		}
		if !ready {
			readyStep := activities.RepositoryStep{StepOrdinal: ordinal, Branch: branch, PushedHead: candidate.PushedHead, PullRequestNumber: pullRequest.Number, PullRequestNodeID: pullRequest.NodeID}
			if err := startTargetStep(ctx, in, readyStep.StepOrdinal, work.StepMarkPullRequestReady); err != nil {
				return fmt.Errorf("starting target pull request readiness: %w", err)
			}
			ordinal++
			if err := session.execute(ctx, func(sessionCtx workflow.Context) error {
				return workflow.ExecuteActivity(sessionCtx, targetRunWorkerActs.TargetMarkPullRequestReady, activities.TargetMarkPullRequestReadyInput{Step: readyStep}).Get(sessionCtx, nil)
			}); err != nil {
				return fmt.Errorf("marking target pull request ready: %w", err)
			}
			completeTrackedStep(ctx, readyStep.StepOrdinal)
			ready = true
		}
		mergeStep = activities.RepositoryStep{StepOrdinal: ordinal, Branch: branch, PushedHead: candidate.PushedHead, PullRequestNumber: pullRequest.Number, PullRequestNodeID: pullRequest.NodeID}
		if err := startTargetStep(ctx, in, mergeStep.StepOrdinal, work.StepMergePullRequest); err != nil {
			return fmt.Errorf("starting target pull request merge: %w", err)
		}
		ordinal++
		mergeOptions, readyToMerge := mergeActivityOptions(ctx, in.Policy.Merge)
		if !readyToMerge {
			var finalized bool
			finalized, runErr = finalizeMergeSemanticDeadline(ctx, session, in, mergeStep.StepOrdinal)
			terminalFinalized = finalized
			return runErr
		}
		if err := session.execute(ctx, func(sessionCtx workflow.Context) error {
			mergeCtx := workflow.WithActivityOptions(sessionCtx, mergeOptions)
			return workflow.ExecuteActivity(mergeCtx, targetRunWorkerActs.TargetMergePullRequest, activities.TargetMergePullRequestInput{Step: mergeStep, ExpectedHeadSHA: candidate.PushedHead}).Get(mergeCtx, &merge)
		}); err != nil {
			if isScheduleToCloseTimeout(err) {
				var finalized bool
				finalized, runErr = finalizeMergeRetryDeadline(ctx, session, in, mergeStep.StepOrdinal, err)
				terminalFinalized = finalized
				return runErr
			}
			return fmt.Errorf("merging reviewed target candidate %s: %w", candidate.PushedHead, err)
		}
		if merge.Outcome == work.PullRequestMergeConfirmed && strings.TrimSpace(merge.MergeSHA) != "" {
			mergeConfirmed = true
			break
		}
		if merge.Outcome == work.PullRequestMergeHeadChanged {
			completeTrackedStep(ctx, mergeStep.StepOrdinal)
			if strings.TrimSpace(merge.PullRequest.HeadSHA) == "" {
				return temporal.NewNonRetryableApplicationError("target merge reported a changed head without its SHA", activities.ErrTypeInvalid, nil)
			}
			updated := merge.PullRequest
			if updated.Number == 0 {
				updated.Number = pullRequest.Number
			}
			if updated.NodeID == "" {
				updated.NodeID = pullRequest.NodeID
			}
			replacementCandidate = &updated
			continue
		}
		if merge.Outcome != work.PullRequestMergeTextConflict && merge.Outcome != work.PullRequestMergeBaseRefreshRequired {
			return temporal.NewNonRetryableApplicationError(fmt.Sprintf("target merge did not confirm candidate %q", candidate.PushedHead), activities.ErrTypeInvalid, nil)
		}
		completeTrackedStep(ctx, mergeStep.StepOrdinal)
		implementTurn++
		feedback = work.AgentPromptContext{CandidateHeadSHA: candidate.PushedHead, Merge: &work.MergeFeedback{Outcome: merge.Outcome, ReviewedHeadSHA: candidate.PushedHead, CurrentHeadSHA: merge.PullRequest.HeadSHA, CurrentBaseSHA: merge.PullRequest.BaseSHA, Diagnostic: merge.Diagnostic}}
		implement, _, err = runTargetAgentStep(ctx, session, in, detail, ordinal, work.AgentStageImplement, work.PriorTurns{Plan: plan.Result, LatestImplement: implement.Result, LatestReview: latestReview}, feedback, implementTurn, implementSeed)
		if err != nil {
			return fmt.Errorf("running target implementation after merge feedback: %w", err)
		}
		implementSeed = targetImplementSeed(implement)
		ordinal++
	}

	terminalCtx, cancelTerminal := workflow.NewDisconnectedContext(ctx)
	defer cancelTerminal()
	finalCtx := workflow.WithActivityOptions(terminalCtx, targetActivityOptions(in.Policy.Recording))
	if err := workflow.ExecuteActivity(finalCtx, targetRecordingActs.FinalizeConfirmedMerge, store.ConfirmedMergeInput{
		RunID: in.RunID, TicketID: in.TicketID, StepOrdinal: mergeStep.StepOrdinal,
		ReviewedHead: mergeStep.PushedHead, MergeSHA: merge.MergeSHA, EndedAt: workflow.Now(terminalCtx),
	}).Get(finalCtx, nil); err != nil {
		return fmt.Errorf("recording confirmed target merge: %w", err)
	}

	session.close()
	session.reportTerminalDelete(terminalCtx, "successful run cleanup")
	return nil
}

func carryForwardHead(recovery activities.CanceledRunCheckpoint) string {
	if !recovery.Found {
		return ""
	}
	return recovery.Checkpoint.PushedHead
}

func canceledRunPullRequestNumber(recovery activities.CanceledRunCheckpoint) int {
	if !recovery.Found {
		return 0
	}
	return recovery.Checkpoint.PullRequestNumber
}

func startTargetStep(ctx workflow.Context, in WorkOnTicketInput, ordinal int, kind work.StepKind) error {
	if err := requireSemanticTime(ctx); err != nil {
		return fmt.Errorf("checking semantic budget before %s step %d: %w", kind, ordinal, err)
	}
	recordingCtx := workflow.WithActivityOptions(ctx, targetActivityOptions(in.Policy.Recording))
	if err := workflow.ExecuteActivity(recordingCtx, targetRecordingActs.StartStep, store.StartStepInput{
		RunID: in.RunID, Ordinal: ordinal, Kind: kind, StartedAt: workflow.Now(ctx),
	}).Get(recordingCtx, nil); err != nil {
		return fmt.Errorf("starting %s step %d: %w", kind, ordinal, err)
	}
	if tracker, ok := ctx.Value(targetStepTrackerContextKey{}).(*targetStepTracker); ok {
		tracker.ordinal, tracker.kind, tracker.active = ordinal, kind, true
	}
	return nil
}

func completeTrackedStep(ctx workflow.Context, ordinal int) {
	if tracker, ok := ctx.Value(targetStepTrackerContextKey{}).(*targetStepTracker); ok && tracker.ordinal == ordinal {
		tracker.active = false
	}
}

func completeInfrastructureStep(ctx workflow.Context, in WorkOnTicketInput, ordinal int, kind string, generation int) error {
	result, err := json.Marshal(struct {
		Kind       string `json:"kind"`
		Generation int    `json:"generation"`
	}{Kind: kind, Generation: generation})
	if err != nil {
		return fmt.Errorf("encoding infrastructure step %d result: %w", ordinal, err)
	}
	recordingCtx := workflow.WithActivityOptions(ctx, targetActivityOptions(in.Policy.Recording))
	if err := workflow.ExecuteActivity(recordingCtx, targetRecordingActs.CompleteStep, in.RunID, ordinal, workflow.Now(ctx), json.RawMessage(result)).Get(recordingCtx, nil); err != nil {
		return fmt.Errorf("completing infrastructure step %d: %w", ordinal, err)
	}
	completeTrackedStep(ctx, ordinal)
	return nil
}

// targetImplementSeed exposes only a terminal implement conversation to a
// later implement turn. Reviews are always independent; a failed child never
// replaces this last known-good lineage.
func targetImplementSeed(result targetAgentStepResult) *agent.ConversationSeed {
	if result.Key.Stage != work.StageImplement || result.Identity == "" || result.ConversationRef.Key == "" {
		return nil
	}
	return &agent.ConversationSeed{
		Source: result.Key, SourceIdentity: result.Identity, ConversationRef: result.ConversationRef,
	}
}

// runTargetAgentStep records one semantic target Attempt before starting its
// reusable child runtime. Activity retries remain internal to that child;
// only a fresh loop iteration below creates another target Attempt row.
func runTargetAgentStep(ctx workflow.Context, session *targetRunSession, in WorkOnTicketInput, detail work.TicketDetail, ordinal int, stage work.AgentStage, prior work.PriorTurns, promptContext work.AgentPromptContext, iteration int, seed *agent.ConversationSeed) (targetAgentStepResult, int, error) {
	if err := promptContext.Validate(); err != nil {
		return targetAgentStepResult{}, 0, temporal.NewNonRetryableApplicationError(fmt.Sprintf("validating %s prompt context: %v", stage, err), activities.ErrTypeInvalid, nil)
	}
	if err := startTargetStep(ctx, in, ordinal, agentStepKind(stage)); err != nil {
		return targetAgentStepResult{}, 0, fmt.Errorf("starting %s agent step: %w", stage, err)
	}
	recordingCtx := workflow.WithActivityOptions(ctx, targetActivityOptions(in.Policy.Recording))
	credentialCtx := workflow.WithActivityOptions(ctx, targetActivityOptions(in.Policy.CredentialRotation))
	attemptStartedAt := make(map[int]time.Time)
	for attemptNo := 1; ; {
		if err := requireSemanticTime(ctx); err != nil {
			return targetAgentStepResult{}, attemptNo - 1, fmt.Errorf("checking semantic budget before %s agent attempt %d: %w", stage, attemptNo, err)
		}
		attemptID := store.TargetAttemptID{RunID: in.RunID, StepOrdinal: ordinal, AttemptNo: attemptNo}
		startedAt, exists := attemptStartedAt[attemptNo]
		if !exists {
			startedAt = workflow.Now(ctx)
			attemptStartedAt[attemptNo] = startedAt
		}
		if err := workflow.ExecuteActivity(recordingCtx, targetRecordingActs.StartAgentAttempt, store.StartAgentAttemptInput{
			ID: attemptID, AgentStage: stage, Model: in.Model, UsageState: work.UsageUnknown, StartedAt: startedAt,
		}).Get(recordingCtx, nil); err != nil {
			return targetAgentStepResult{}, attemptNo - 1, fmt.Errorf("starting %s agent attempt: %w", stage, err)
		}
		var revision work.RunWorkerCredentialRevision
		if err := workflow.ExecuteActivity(credentialCtx, runWorkerControlActs.RotateRunWorkerGitHubCredential, activities.RotateRunWorkerGitHubCredentialInput{Identity: session.identity}).Get(credentialCtx, &revision); err != nil {
			return targetAgentStepResult{}, attemptNo, fmt.Errorf("rotating %s GitHub credential: %w", stage, err)
		}
		if err := revision.Validate(); err != nil {
			return targetAgentStepResult{}, attemptNo, fmt.Errorf("validating %s credential revision: %w", stage, err)
		}
		key := work.StageKey{Ticket: detail.Number, RunID: in.RunID, Stage: work.Stage(stage), Turn: iteration}
		identity := fmt.Sprintf("agent/%s/step/%d/attempt/%d", in.RunID, ordinal, attemptNo)
		child := workflow.WithChildOptions(ctx, targetAgentChildOptions(identity, in.Policy.Agent))
		var result AgentWorkflowResult
		attempt := activities.StageAttempt{Key: key, Model: in.Model, Detail: detail, Prior: prior, PromptContext: promptContext}
		childInput := AgentWorkflowInput{
			Attempt: attempt, ToolsetID: targetToolset(stage),
			ToolTarget:      agent.ToolTarget{Kind: agent.ToolTargetRunWorker, RunWorkerIdentity: session.identity},
			ModelTurnPolicy: in.Policy.Agent, ControlPolicy: in.Policy.Recording, CacheKey: identity, Identity: identity, Seed: seed,
		}
		if targetUsesLegacyPayload(ctx) {
			childInput.Attempt.LegacyMaxReviewSteps = legacyTargetMaxReviewSteps
			childInput.LegacyLimits = defaultLegacyAgentLimits()
		}
		childFuture := workflow.ExecuteChildWorkflow(child, AgentWorkflow, childInput)
		renewalCtx, cancelRenewal := workflow.WithCancel(ctx)
		var childErr error
		for {
			completed := false
			selector := workflow.NewSelector(ctx)
			selector.AddFuture(childFuture, func(f workflow.Future) {
				completed = true
				childErr = f.Get(ctx, &result)
			})
			selector.AddFuture(workflow.NewTimer(renewalCtx, credentialRenewalInterval), func(workflow.Future) {})
			selector.Select(ctx)
			if completed {
				break
			}
			if err := workflow.ExecuteActivity(credentialCtx, runWorkerControlActs.RotateRunWorkerGitHubCredential, activities.RotateRunWorkerGitHubCredentialInput{Identity: session.identity}).Get(credentialCtx, &revision); err != nil {
				cancelRenewal()
				return targetAgentStepResult{}, attemptNo, fmt.Errorf("renewing %s GitHub credential: %w", stage, err)
			}
			if err := revision.Validate(); err != nil {
				cancelRenewal()
				return targetAgentStepResult{}, attemptNo, fmt.Errorf("validating renewed %s credential revision: %w", stage, err)
			}
		}
		cancelRenewal()
		if childErr != nil {
			if isRunWorkerSessionLoss(childErr) {
				if replaceErr := session.replace(ctx); replaceErr != nil {
					return targetAgentStepResult{}, attemptNo, fmt.Errorf("replacing lost Run Worker Session: %w", replaceErr)
				}
				continue
			}
			if failErr := workflow.ExecuteActivity(recordingCtx, targetRecordingActs.FailAgentAttempt, store.AgentAttemptFailureInput{ID: attemptID, FailureKind: work.RunFailureInfrastructure, EndedAt: workflow.Now(ctx)}).Get(recordingCtx, nil); failErr != nil {
				return targetAgentStepResult{}, attemptNo, fmt.Errorf("recording failed %s agent attempt: %w", stage, failErr)
			}
			return targetAgentStepResult{}, attemptNo, fmt.Errorf("running %s agent attempt: %w", stage, childErr)
		}
		if result.Failure != nil {
			if result.Failure.Is(agent.TerminalFailureSessionLost) {
				if result.TranscriptRef.Key != "" {
					if evidenceErr := workflow.ExecuteActivity(recordingCtx, activities.TargetAgentEvidenceFinalizeActivityName, activities.TargetAgentEvidenceInput{
						AttemptID: attemptID, Identity: identity, State: work.AgentAttemptRunning,
						Usage: result.Usage, UsageMeasured: result.UsageMeasured, TranscriptRef: result.TranscriptRef,
					}).Get(recordingCtx, nil); evidenceErr != nil {
						return targetAgentStepResult{}, attemptNo, fmt.Errorf("recording interrupted %s agent evidence: %w", stage, evidenceErr)
					}
				}
				if replaceErr := session.replace(ctx); replaceErr != nil {
					return targetAgentStepResult{}, attemptNo, fmt.Errorf("replacing lost Run Worker Session: %w", replaceErr)
				}
				// A worker loss retains this semantic Attempt. The replacement
				// reconciles repository state before the child is started again.
				continue
			}
			endedAt := workflow.Now(ctx)
			if result.TranscriptRef.Key != "" {
				if evidenceErr := workflow.ExecuteActivity(recordingCtx, activities.TargetAgentEvidenceFinalizeActivityName, activities.TargetAgentEvidenceInput{
					AttemptID: attemptID, Identity: identity, State: work.AgentAttemptFailed, FailureKind: work.RunFailureAgentUnrecoverable,
					Usage: result.Usage, UsageMeasured: result.UsageMeasured, TranscriptRef: result.TranscriptRef, EndedAt: endedAt,
				}).Get(recordingCtx, nil); evidenceErr != nil {
					return targetAgentStepResult{}, attemptNo, fmt.Errorf("recording terminal %s agent evidence: %w", stage, evidenceErr)
				}
			} else if failErr := workflow.ExecuteActivity(recordingCtx, targetRecordingActs.FailAgentAttempt, store.AgentAttemptFailureInput{
				ID: attemptID, FailureKind: work.RunFailureAgentUnrecoverable, EndedAt: endedAt,
			}).Get(recordingCtx, nil); failErr != nil {
				return targetAgentStepResult{}, attemptNo, fmt.Errorf("recording terminal %s agent failure: %w", stage, failErr)
			}
			if targetFailureNeedsFreshAttempt(result.Failure) {
				attemptNo++
				continue
			}
			return targetAgentStepResult{}, attemptNo, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("%s agent terminal failure: %s", stage, result.Failure.Kind),
				activities.ErrTypeUnresumableIncompleteAttempt,
				nil,
			)
		}
		if result.Result.Value() == nil {
			return targetAgentStepResult{}, attemptNo, temporal.NewNonRetryableApplicationError(fmt.Sprintf("%s agent produced no durable result", stage), activities.ErrTypeInvalid, nil)
		}
		endedAt := workflow.Now(ctx)
		if err := workflow.ExecuteActivity(recordingCtx, activities.TargetAgentEvidenceFinalizeActivityName, activities.TargetAgentEvidenceInput{
			AttemptID: attemptID, Identity: identity, State: work.AgentAttemptSucceeded, Result: &result.Result, Usage: result.Usage,
			UsageMeasured: result.UsageMeasured, TranscriptRef: result.TranscriptRef, EndedAt: endedAt,
		}).Get(recordingCtx, nil); err != nil {
			return targetAgentStepResult{}, attemptNo, fmt.Errorf("checkpointing %s agent evidence: %w", stage, err)
		}
		raw, err := json.Marshal(result.Result)
		if err != nil {
			return targetAgentStepResult{}, attemptNo, fmt.Errorf("encoding %s agent result: %w", stage, err)
		}
		if err := workflow.ExecuteActivity(recordingCtx, targetRecordingActs.CompleteStep, in.RunID, ordinal, endedAt, raw).Get(recordingCtx, nil); err != nil {
			return targetAgentStepResult{}, attemptNo, fmt.Errorf("completing %s step %d: %w", stage, ordinal, err)
		}
		completeTrackedStep(ctx, ordinal)
		return targetAgentStepResult{Key: key, Identity: identity, Raw: raw, Result: result.Result, Usage: result.Usage, UsageMeasured: result.UsageMeasured, ConversationRef: result.ConversationRef, TranscriptRef: result.TranscriptRef}, attemptNo, nil
	}
}

func targetUsesLegacyPayload(ctx workflow.Context) bool {
	legacy, ok := ctx.Value(targetLegacyPayloadContextKey{}).(bool)
	return ok && legacy
}

func targetFailureNeedsFreshAttempt(failure *agent.TerminalFailure) bool {
	return failure.Is(agent.TerminalFailureAmbiguousToolExecution) ||
		failure.Is(agent.TerminalFailureInvalidProviderOutcome)
}

func targetAgentChildOptions(identity string, policy work.AgentActivityPolicy) workflow.ChildWorkflowOptions {
	return workflow.ChildWorkflowOptions{
		WorkflowID: identity, WorkflowExecutionTimeout: policy.ScheduleToCloseTimeout,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WaitForCancellation:   true, ParentClosePolicy: enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
	}
}

func targetToolset(stage work.AgentStage) agent.ToolsetID {
	if stage == work.AgentStageImplement {
		return agent.ToolsetCodingWriteV1
	}
	return agent.ToolsetCodingReadV1
}

// targetRunSession owns one live Run Worker generation. It is the only
// workflow-local state allowed to replace that worker, which serializes loss
// recovery and prevents two generations from receiving repository work.
type targetRunSession struct {
	in            WorkOnTicketInput
	ticketNumber  int
	branch        string
	identity      work.RunWorkerIdentity
	sessionCtx    workflow.Context
	open          bool
	checkoutReady bool
	hardDeadline  time.Time
}

func newTargetRunSession(ctx workflow.Context, in WorkOnTicketInput, ticketNumber int, branch string, hardDeadline time.Time) (*targetRunSession, error) {
	session := &targetRunSession{in: in, ticketNumber: ticketNumber, branch: branch, hardDeadline: hardDeadline}
	if err := session.provisionAndCreate(ctx, 1, true); err != nil {
		return nil, fmt.Errorf("provisioning initial Run Worker generation: %w", err)
	}
	return session, nil
}

func (s *targetRunSession) provisionAndCreate(ctx workflow.Context, generation int, recordPreparationSteps bool) error {
	identity, err := work.NewRunWorkerIdentity(s.in.RunID, generation)
	if err != nil {
		return temporal.NewNonRetryableApplicationError(fmt.Sprintf("the target Run %q cannot own a Run Worker: %v", s.in.RunID, err), activities.ErrTypeInvalid, nil)
	}
	if recordPreparationSteps {
		if err := startTargetStep(ctx, s.in, 1, work.StepCreateRunWorker); err != nil {
			return fmt.Errorf("starting Run Worker creation: %w", err)
		}
	}
	controlCtx := workflow.WithActivityOptions(ctx, targetActivityOptions(s.in.Policy.Provisioning))
	if err := workflow.ExecuteActivity(controlCtx, runWorkerControlActs.ProvisionRunWorker, activities.ProvisionRunWorkerInput{
		TicketNumber: s.ticketNumber, Identity: identity, Branch: s.branch,
	}).Get(controlCtx, nil); err != nil {
		return fmt.Errorf("provisioning Run Worker generation %d: %w", generation, err)
	}
	if recordPreparationSteps {
		if err := completeInfrastructureStep(ctx, s.in, 1, "created", generation); err != nil {
			return s.cleanupProvisionFailure(ctx, identity, err)
		}
		if err := startTargetStep(ctx, s.in, 2, work.StepAcquireRunWorkerSession); err != nil {
			return s.cleanupProvisionFailure(ctx, identity, err)
		}
	}
	privateQueue, err := work.RunWorkerTaskQueue(identity)
	if err != nil {
		if deleteErr := s.deleteIdentity(ctx, identity); deleteErr != nil {
			return errors.Join(fmt.Errorf("building Run Worker private task queue: %w", err), fmt.Errorf("deleting provisioned Run Worker generation %d: %w", generation, deleteErr))
		}
		return fmt.Errorf("building Run Worker private task queue: %w", err)
	}
	sessionOptions := targetActivityOptions(s.in.Policy.Provisioning)
	sessionOptions.TaskQueue = privateQueue
	executionTimeout, err := remainingSessionExecutionTimeout(workflow.Now(ctx), s.hardDeadline)
	if err != nil {
		return s.cleanupProvisionFailure(ctx, identity, err)
	}
	sessionCtx, err := workflow.CreateSession(workflow.WithActivityOptions(ctx, sessionOptions), &workflow.SessionOptions{
		ExecutionTimeout: executionTimeout, CreationTimeout: s.in.Policy.Provisioning.ScheduleToCloseTimeout, HeartbeatTimeout: s.in.Policy.Agent.HeartbeatTimeout,
	})
	if err != nil {
		sessionErr := temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("creating Run Worker Session for generation %d", generation),
			activities.ErrTypeRunWorkerSessionLost,
			err,
		)
		if deleteErr := s.deleteIdentity(ctx, identity); deleteErr != nil {
			return errors.Join(sessionErr, fmt.Errorf("deleting provisioned Run Worker generation %d: %w", generation, deleteErr))
		}
		return sessionErr
	}
	if recordPreparationSteps {
		if err := completeInfrastructureStep(ctx, s.in, 2, "acquired", generation); err != nil {
			workflow.CompleteSession(sessionCtx)
			return s.cleanupProvisionFailure(ctx, identity, err)
		}
	}
	s.identity, s.sessionCtx, s.open = identity, sessionCtx, true
	return nil
}

func (s *targetRunSession) cleanupProvisionFailure(ctx workflow.Context, identity work.RunWorkerIdentity, cause error) error {
	if deleteErr := s.deleteIdentity(ctx, identity); deleteErr != nil {
		return errors.Join(cause, fmt.Errorf("deleting provisioned Run Worker generation %d: %w", identity.Generation, deleteErr))
	}
	return fmt.Errorf("cleaning up failed Run Worker generation %d provisioning: %w", identity.Generation, cause)
}

func remainingSessionExecutionTimeout(now, hardDeadline time.Time) (time.Duration, error) {
	remaining := hardDeadline.Sub(now)
	if remaining <= 0 {
		return 0, temporal.NewNonRetryableApplicationError("target run reached its absolute hard deadline", activities.ErrTypeHardDeadline, nil)
	}
	return remaining, nil
}

// WorkOnTicketExecutionTimeout is the absolute child-workflow ceiling that the
// dispatcher must apply when it starts WorkOnTicket.
func WorkOnTicketExecutionTimeout(policy work.TargetRunPolicy) time.Duration {
	return policy.HardDeadline
}

func (s *targetRunSession) execute(ctx workflow.Context, run func(workflow.Context) error) error {
	for {
		err := run(s.sessionCtx)
		if !isRunWorkerSessionLoss(err) {
			if err != nil {
				return fmt.Errorf("executing Run Worker Session activity: %w", err)
			}
			return nil
		}
		if err := s.replace(ctx); err != nil {
			return fmt.Errorf("replacing lost Run Worker Session: %w", err)
		}
	}
}

func (s *targetRunSession) replace(ctx workflow.Context) error {
	for {
		s.close()
		if err := s.delete(ctx); err != nil {
			return fmt.Errorf("deleting lost Run Worker generation %d before replacement: %w", s.identity.Generation, err)
		}
		nextGeneration := s.identity.Generation + 1
		if err := s.provisionAndCreate(ctx, nextGeneration, false); err != nil {
			return fmt.Errorf("provisioning replacement Run Worker generation %d: %w", nextGeneration, err)
		}
		if !s.checkoutReady {
			return nil
		}
		err := workflow.ExecuteActivity(s.sessionCtx, targetRunWorkerActs.RestoreTargetRepository, activities.RestoreTargetRepositoryInput{
			CloneURL: s.in.CloneURL, Branch: s.branch,
		}).Get(s.sessionCtx, nil)
		if err == nil {
			return nil
		}
		if !isRunWorkerSessionLoss(err) {
			return fmt.Errorf("restoring replacement repository: %w", err)
		}
	}
}

func (s *targetRunSession) close() {
	if s.open {
		workflow.CompleteSession(s.sessionCtx)
		s.open = false
	}
}

func (s *targetRunSession) delete(ctx workflow.Context) error {
	return s.deleteIdentity(ctx, s.identity)
}

func (s *targetRunSession) deleteIdentity(ctx workflow.Context, identity work.RunWorkerIdentity) error {
	teardownCtx := workflow.WithActivityOptions(ctx, targetActivityOptions(s.in.Policy.Teardown))
	return workflow.ExecuteActivity(teardownCtx, runWorkerControlActs.DeleteRunWorker, activities.DeleteRunWorkerInput{Identity: identity}).Get(teardownCtx, nil)
}

func (s *targetRunSession) reportTerminalDelete(ctx workflow.Context, operation string) {
	if err := s.delete(ctx); err != nil {
		workflow.GetLogger(ctx).Error(operation+" exhausted bounded teardown; maintenance owns the orphan", "run_id", s.identity.RunID, "generation", s.identity.Generation, "error", err)
	}
}

func isRunWorkerSessionLoss(err error) bool {
	if errors.Is(err, workflow.ErrSessionFailed) {
		return true
	}
	var application *temporal.ApplicationError
	return errors.As(err, &application) && application.Type() == activities.ErrTypeRunWorkerSessionLost
}

func requireSemanticTime(ctx workflow.Context) error {
	deadline, ok := ctx.Value(semanticDeadlineContextKey{}).(time.Time)
	if !ok {
		return temporal.NewNonRetryableApplicationError("target run semantic deadline is unavailable", activities.ErrTypeInvalid, nil)
	}
	if !workflow.Now(ctx).Before(deadline) {
		return temporal.NewNonRetryableApplicationError("target run reached its semantic deadline", activities.ErrTypeSemanticDeadline, nil)
	}
	return nil
}

func semanticTimeRemaining(ctx workflow.Context) bool {
	deadline, ok := ctx.Value(semanticDeadlineContextKey{}).(time.Time)
	return ok && workflow.Now(ctx).Before(deadline)
}

func semanticTimeRemainingDuration(ctx workflow.Context) time.Duration {
	deadline, ok := ctx.Value(semanticDeadlineContextKey{}).(time.Time)
	if !ok {
		return 0
	}
	return deadline.Sub(workflow.Now(ctx))
}

func isScheduleToCloseTimeout(err error) bool {
	var timeout *temporal.TimeoutError
	return errors.As(err, &timeout) && timeout.TimeoutType() == enums.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE
}

func finalizeUnobservedCI(ctx workflow.Context, session *targetRunSession, in WorkOnTicketInput, stepOrdinal int) error {
	terminalCtx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	finalCtx := workflow.WithActivityOptions(terminalCtx, targetActivityOptions(in.Policy.Recording))
	if err := workflow.ExecuteActivity(finalCtx, targetRecordingActs.FinalizeRunFailure, store.RunFailureInput{
		RunID:       in.RunID,
		TicketID:    in.TicketID,
		Outcome:     work.RunOutcomeFailed,
		FailureKind: work.RunFailureCIUnobserved,
		StepOrdinal: stepOrdinal,
		StepResult:  json.RawMessage(`{"kind":"ci_unobserved"}`),
		EndedAt:     workflow.Now(terminalCtx),
	}).Get(finalCtx, nil); err != nil {
		return fmt.Errorf("recording unobserved CI: %w", err)
	}
	session.close()
	session.reportTerminalDelete(terminalCtx, "unobserved CI cleanup")
	return temporal.NewNonRetryableApplicationError("target CI became unobserved", activities.ErrTypeCIUnobserved, nil)
}

func mergeActivityOptions(ctx workflow.Context, policy work.ActivityPolicy) (workflow.ActivityOptions, bool) {
	remaining := semanticTimeRemainingDuration(ctx)
	if remaining <= 0 {
		return workflow.ActivityOptions{}, false
	}
	policy.ScheduleToCloseTimeout = remaining
	return targetActivityOptions(policy), true
}

func finalizeMergeRetryDeadline(ctx workflow.Context, session *targetRunSession, in WorkOnTicketInput, stepOrdinal int, mergeErr error) (bool, error) {
	failureKind, stepResult, errorType := mergeRetryDeadlineFailure(mergeErr)
	return finalizeMergeFailure(ctx, session, in, stepOrdinal, failureKind, stepResult, errorType, "target merge remained unavailable through its retry window")
}

func finalizeMergeSemanticDeadline(ctx workflow.Context, session *targetRunSession, in WorkOnTicketInput, stepOrdinal int) (bool, error) {
	return finalizeMergeFailure(ctx, session, in, stepOrdinal, work.RunFailureSemanticDeadline, json.RawMessage(`{"kind":"semantic_deadline"}`), activities.ErrTypeSemanticDeadline, "target run reached its semantic deadline before scheduling merge")
}

func finalizeMergeFailure(ctx workflow.Context, session *targetRunSession, in WorkOnTicketInput, stepOrdinal int, failureKind work.RunFailureKind, stepResult json.RawMessage, errorType, message string) (bool, error) {
	terminalErr := &terminalRunError{
		err:         temporal.NewNonRetryableApplicationError(message, errorType, nil),
		outcome:     work.RunOutcomeFailed,
		failureKind: failureKind,
		stepOrdinal: stepOrdinal,
		stepResult:  stepResult,
	}
	terminalCtx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	finalCtx := workflow.WithActivityOptions(terminalCtx, targetActivityOptions(in.Policy.Recording))
	if err := workflow.ExecuteActivity(finalCtx, targetRecordingActs.FinalizeRunFailure, store.RunFailureInput{
		RunID:       in.RunID,
		TicketID:    in.TicketID,
		Outcome:     work.RunOutcomeFailed,
		FailureKind: failureKind,
		StepOrdinal: stepOrdinal,
		StepResult:  stepResult,
		EndedAt:     workflow.Now(terminalCtx),
	}).Get(finalCtx, nil); err != nil {
		workflow.GetLogger(ctx).Error("initial target merge failure recording exhausted; deferred finalization will retry", "run_id", in.RunID, "failure_kind", failureKind, "error", err)
		return false, terminalErr
	}
	session.close()
	session.reportTerminalDelete(terminalCtx, "merge failure cleanup")
	return true, terminalErr.err
}

func mergeRetryDeadlineFailure(err error) (work.RunFailureKind, json.RawMessage, string) {
	var application *temporal.ApplicationError
	if errors.As(err, &application) && application.Type() == activities.ErrTypeRuleset {
		return work.RunFailureGitHubRuleset, json.RawMessage(`{"kind":"github_ruleset"}`), activities.ErrTypeRuleset
	}
	return work.RunFailureGitHubUnavailable, json.RawMessage(`{"kind":"github_unavailable"}`), activities.ErrTypeTransient
}

func terminalFailureKind(err error) (work.RunOutcome, work.RunFailureKind, bool) {
	if err == nil || temporal.IsCanceledError(err) {
		return "", work.RunFailureNone, false
	}
	if isRunWorkerSessionLoss(err) {
		return work.RunOutcomeFailed, work.RunFailureRunWorkerUnavailable, true
	}
	var terminal *terminalRunError
	if errors.As(err, &terminal) {
		return terminal.outcome, terminal.failureKind, true
	}
	var application *temporal.ApplicationError
	if errors.As(err, &application) {
		switch application.Type() {
		case activities.ErrTypePredecessorMergeFenced:
			return "", work.RunFailureNone, false
		case activities.ErrTypeInvalid:
			return work.RunOutcomeFailed, work.RunFailureInvalidInput, true
		case activities.ErrTypeUnresumableIncompleteAttempt:
			return work.RunOutcomeFailed, work.RunFailureAgentUnrecoverable, true
		case activities.ErrTypeCIUnobserved:
			return work.RunOutcomeFailed, work.RunFailureCIUnobserved, true
		case activities.ErrTypeAuth:
			return work.RunOutcomeFailed, work.RunFailureGitHubAuth, true
		case activities.ErrTypeRuleset:
			return work.RunOutcomeFailed, work.RunFailureGitHubRuleset, true
		case activities.ErrTypeHardDeadline, activities.ErrTypeRunWorkerSessionLost:
			return work.RunOutcomeFailed, work.RunFailureRunWorkerUnavailable, true
		case activities.ErrTypeSemanticDeadline:
			return work.RunOutcomeFailed, work.RunFailureSemanticDeadline, true
		}
	}
	return work.RunOutcomeFailed, work.RunFailureInfrastructure, true
}

func agentStepKind(stage work.AgentStage) work.StepKind {
	switch stage {
	case work.AgentStagePlan:
		return work.StepPlan
	case work.AgentStageImplement:
		return work.StepImplement
	case work.AgentStageReview:
		return work.StepReview
	default:
		return ""
	}
}

func optionalPullRequest(pullRequest work.PullRequest) *work.PullRequest {
	if pullRequest.Number <= 0 {
		return nil
	}
	return &pullRequest
}

func implementTitle(out work.StageOutput) string {
	implemented, ok := out.Value().(work.ImplementOutput)
	if !ok {
		return ""
	}
	return implemented.Title
}

func implementBody(out work.StageOutput) string {
	implemented, ok := out.Value().(work.ImplementOutput)
	if !ok {
		return ""
	}
	return implemented.Body
}

func validateWorkOnTicket(in WorkOnTicketInput) error {
	if in.TicketID <= 0 {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("ticket id %d is not a target Ticket", in.TicketID),
			activities.ErrTypeInvalid,
			nil,
		)
	}
	if err := in.Policy.Validate(); err != nil {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("the target run policy for ticket %d is unusable: %v", in.TicketID, err),
			activities.ErrTypeInvalid,
			nil,
		)
	}
	if strings.TrimSpace(in.CloneURL) == "" {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("ticket %d has no repository clone URL", in.TicketID),
			activities.ErrTypeInvalid,
			nil,
		)
	}
	if err := in.Model.Validate(); err != nil {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("the target model for ticket %d is unusable: %v", in.TicketID, err),
			activities.ErrTypeInvalid,
			nil,
		)
	}
	return nil
}

func targetActivityOptions(policy work.ActivityPolicy) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    policy.StartToCloseTimeout,
		ScheduleToCloseTimeout: policy.ScheduleToCloseTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    policy.Retry.InitialInterval,
			BackoffCoefficient: policy.Retry.BackoffCoefficient,
			MaximumInterval:    policy.Retry.MaximumInterval,
			MaximumAttempts:    policy.Retry.MaximumAttempts,
		},
	}
}
