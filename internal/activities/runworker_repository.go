package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/temporal"
)

const (
	repositoryEffectClone = "clone"
	repositoryEffectCI    = "await_ci"
	repositoryEffectFind  = "find_pull_request"
	repositoryEffectSync  = "sync_pull_request"
	repositoryEffectReady = "mark_pull_request_ready"
	repositoryEffectMerge = "merge_pull_request"
)

// RepositoryStep is the complete non-secret Git/PR recovery position carried
// between repository-affine Steps. StepOrdinal identifies the result this
// activity must durably complete before acknowledging success.
type RepositoryStep struct {
	StepOrdinal       int
	Branch            string
	PushedHead        string
	ObservedBase      string
	PullRequestNumber int
	PullRequestNodeID string
}

type repositoryEffectEnvelope struct {
	Kind   string          `json:"kind"`
	Result json.RawMessage `json:"result"`
}

// CloneTargetRepositoryInput identifies the repository Step and source to
// restore on this Run Worker generation.
type CloneTargetRepositoryInput struct {
	Step                    RepositoryStep
	CloneURL                string
	CarryForwardHead        string
	RetirePullRequestNumber int
}

// CloneTargetRepositoryOutput records the exact restored candidate head.
type CloneTargetRepositoryOutput struct {
	HeadSHA          string
	PredecessorMerge *work.PullRequestRetirement
}

// RestoreTargetRepositoryInput names the durable branch a replacement
// generation must materialize before resuming repository-affine work.
type RestoreTargetRepositoryInput struct {
	CloneURL string
	Branch   string
}

// CloneTargetRepository is the first Session-bound operation. It restores the
// local checkout, then completes its infrastructure Step before returning.
func (a *RunWorkerActivities) CloneTargetRepository(ctx context.Context, in CloneTargetRepositoryInput) (CloneTargetRepositoryOutput, error) {
	cp, raw, found, err := a.loadRepositoryResult(ctx, in.Step, repositoryEffectClone)
	if err != nil {
		return CloneTargetRepositoryOutput{}, fmt.Errorf("loading target repository clone effect: %w", err)
	}
	head, predecessorMerge, err := a.prepareCloneRepository(ctx, in)
	if err != nil {
		return CloneTargetRepositoryOutput{}, fail(ctx, "preparing the target repository", err)
	}
	if predecessorMerge != nil {
		return CloneTargetRepositoryOutput{PredecessorMerge: predecessorMerge}, nil
	}
	if found {
		// A repository checkpoint survives replacement; this filesystem does
		// not. Always restore locally, but return the exact durable Step result
		// and never checkpoint/publish that completed effect a second time.
		return decodeRepositoryResult[CloneTargetRepositoryOutput](raw)
	}
	out := CloneTargetRepositoryOutput{HeadSHA: head}
	position := in.Step
	position.PushedHead = head
	if err := a.checkpointRepositoryResult(ctx, cp, position, repositoryEffectClone, out); err != nil {
		return CloneTargetRepositoryOutput{}, fmt.Errorf("checkpointing target repository clone effect: %w", err)
	}
	return out, nil
}

func (a *RunWorkerActivities) prepareCloneRepository(ctx context.Context, in CloneTargetRepositoryInput) (string, *work.PullRequestRetirement, error) {
	if in.RetirePullRequestNumber > 0 {
		retirement, err := a.deps.GitHub.RetirePullRequest(ctx, in.RetirePullRequestNumber)
		if err != nil {
			return "", nil, fmt.Errorf("retiring the canceled run pull request: %w", err)
		}
		if retirement.Merged {
			return "", &retirement, nil
		}
	}
	if strings.TrimSpace(in.CarryForwardHead) == "" {
		head, err := a.deps.Repository.Prepare(ctx, in.CloneURL, in.Step.Branch)
		if err != nil {
			return "", nil, fmt.Errorf("preparing the target repository checkout: %w", err)
		}
		return head, nil, nil
	}
	head, err := a.deps.Repository.PrepareFromCommit(ctx, in.CloneURL, in.Step.Branch, in.CarryForwardHead)
	if err != nil {
		return "", nil, fmt.Errorf("preparing the target repository from the durable recovery commit: %w", err)
	}
	return head, nil, nil
}

// RestoreTargetRepository reconstructs a replacement filesystem from the
// newest durable Git checkpoint without opening another Step or repeating a
// GitHub effect.
func (a *RunWorkerActivities) RestoreTargetRepository(ctx context.Context, in RestoreTargetRepositoryInput) error {
	if err := a.validateRepositoryBranch(ctx, in.Branch); err != nil {
		return err
	}
	if strings.TrimSpace(in.CloneURL) == "" || strings.TrimSpace(in.Branch) == "" {
		return fail(ctx, "validating replacement repository restore", fmt.Errorf("clone URL and branch are required: %w", work.ErrPermanent))
	}
	cp, err := a.deps.RepositoryCheckpoints(a.deps.Identity)
	if err != nil {
		return fail(ctx, "opening replacement repository checkpoint", err)
	}
	checkpoint, found, err := cp.Load(ctx)
	if err != nil {
		return fail(ctx, "loading replacement repository checkpoint", err)
	}
	if !found || checkpoint.Branch != in.Branch {
		return fail(ctx, "reconciling replacement repository checkpoint", fmt.Errorf("durable branch is unavailable or does not match replacement: %w", work.ErrPermanent))
	}
	if _, err := a.deps.Repository.Prepare(ctx, in.CloneURL, in.Branch); err != nil {
		return fail(ctx, "restoring the replacement repository", err)
	}
	return nil
}

// TargetAwaitCIInput binds a CI observation to one durable repository Step.
type TargetAwaitCIInput struct {
	Step RepositoryStep
	CI   AwaitCIInput
}

// TargetAwaitCI observes required checks for the exact durable candidate head.
func (a *RunWorkerActivities) TargetAwaitCI(ctx context.Context, in TargetAwaitCIInput) (AwaitCIOutput, error) {
	cp, raw, found, err := a.loadRepositoryResult(ctx, in.Step, repositoryEffectCI)
	if err != nil {
		return AwaitCIOutput{}, fmt.Errorf("loading target CI effect: %w", err)
	}
	if found {
		return decodeRepositoryResult[AwaitCIOutput](raw)
	}
	if err := validateAwaitCIInput(in.CI); err != nil {
		return AwaitCIOutput{}, fail(ctx, "awaiting target CI", err)
	}
	if in.Step.PushedHead != in.CI.CommitSHA {
		return AwaitCIOutput{}, fail(ctx, "awaiting target CI", fmt.Errorf("candidate SHA does not match the repository position: %w", work.ErrPermanent))
	}
	checks, err := a.deps.GitHub.ChecksForCommit(ctx, in.CI.CommitSHA, in.CI.RequiredChecks)
	if err != nil {
		return AwaitCIOutput{}, fail(ctx, fmt.Sprintf("awaiting target CI for commit %s", in.CI.CommitSHA), err)
	}
	green, failures := reduceRequiredChecks(checks, in.CI.RequiredChecks)
	if !green && failures == nil {
		return AwaitCIOutput{}, temporal.NewApplicationErrorWithOptions(
			fmt.Sprintf("target CI for commit %s has not concluded for every required check", in.CI.CommitSHA),
			ErrTypeCINotConcluded, temporal.ApplicationErrorOptions{NextRetryDelay: awaitCIRetryDelay})
	}
	out := AwaitCIOutput{CommitSHA: in.CI.CommitSHA, Green: green, RedFailures: failures}
	if err := a.checkpointRepositoryResult(ctx, cp, in.Step, repositoryEffectCI, out); err != nil {
		return AwaitCIOutput{}, fmt.Errorf("checkpointing target CI effect: %w", err)
	}
	return out, nil
}

// TargetFindPullRequestInput binds PR discovery to one repository Step.
type TargetFindPullRequestInput struct{ Step RepositoryStep }

// FindPullRequestOutput reports whether the branch already owns a pull request.
type FindPullRequestOutput struct {
	PullRequest work.PullRequest
	Found       bool
}

// TargetFindPullRequest discovers the PR associated with the durable branch.
func (a *RunWorkerActivities) TargetFindPullRequest(ctx context.Context, in TargetFindPullRequestInput) (FindPullRequestOutput, error) {
	cp, raw, found, err := a.loadRepositoryResult(ctx, in.Step, repositoryEffectFind)
	if err != nil {
		return FindPullRequestOutput{}, fmt.Errorf("loading target pull request discovery effect: %w", err)
	}
	if found {
		return decodeRepositoryResult[FindPullRequestOutput](raw)
	}
	pr, exists, err := a.deps.GitHub.PullRequestForBranch(ctx, in.Step.Branch)
	if err != nil {
		return FindPullRequestOutput{}, fail(ctx, "finding the target pull request", err)
	}
	out := FindPullRequestOutput{PullRequest: pr, Found: exists}
	position := in.Step
	if exists {
		position.PullRequestNumber, position.PullRequestNodeID = pr.Number, pr.NodeID
	}
	if err := a.checkpointRepositoryResult(ctx, cp, position, repositoryEffectFind, out); err != nil {
		return FindPullRequestOutput{}, fmt.Errorf("checkpointing target pull request discovery effect: %w", err)
	}
	return out, nil
}

// TargetSyncPullRequestInput carries the durable Step and desired PR content.
type TargetSyncPullRequestInput struct {
	Step     RepositoryStep
	Title    string
	Body     string
	Existing *work.PullRequest
}

// TargetSyncPullRequest opens or updates the branch PR and checkpoints it.
func (a *RunWorkerActivities) TargetSyncPullRequest(ctx context.Context, in TargetSyncPullRequestInput) (work.PullRequest, error) {
	// Synchronization discovers the authoritative pushed head from GitHub. A
	// retry begins from the preceding repository position, so its input still
	// carries that old head while the completed checkpoint carries the newly
	// observed one.
	recoveryStep := in.Step
	recoveryStep.PushedHead = ""
	cp, raw, found, err := a.loadRepositoryResult(ctx, recoveryStep, repositoryEffectSync)
	if err != nil {
		return work.PullRequest{}, fmt.Errorf("loading target pull request sync effect: %w", err)
	}
	if found {
		return decodeRepositoryResult[work.PullRequest](raw)
	}
	if strings.TrimSpace(in.Title) == "" {
		return work.PullRequest{}, fail(ctx, "synchronizing the target pull request", fmt.Errorf("title is required: %w", work.ErrPermanent))
	}
	publishedHead, err := a.deps.Repository.Publish(ctx, a.deps.Branch)
	if err != nil {
		return work.PullRequest{}, fail(ctx, "publishing the target pull request candidate", err)
	}
	pr, err := a.deps.GitHub.OpenOrUpdatePullRequest(ctx, in.Step.Branch, in.Title, in.Body, in.Existing)
	if err != nil {
		return work.PullRequest{}, fail(ctx, "synchronizing the target pull request", err)
	}
	pr.HeadSHA = publishedHead
	position := in.Step
	position.PushedHead = publishedHead
	position.PullRequestNumber, position.PullRequestNodeID = pr.Number, pr.NodeID
	if err := a.checkpointRepositoryResult(ctx, cp, position, repositoryEffectSync, pr); err != nil {
		return work.PullRequest{}, fmt.Errorf("checkpointing target pull request sync effect: %w", err)
	}
	return pr, nil
}

// TargetMarkPullRequestReadyInput identifies the PR Step to make reviewable.
type TargetMarkPullRequestReadyInput struct{ Step RepositoryStep }

// TargetMarkPullRequestReady removes draft status and checkpoints the effect.
func (a *RunWorkerActivities) TargetMarkPullRequestReady(ctx context.Context, in TargetMarkPullRequestReadyInput) error {
	cp, _, found, err := a.loadRepositoryResult(ctx, in.Step, repositoryEffectReady)
	if err != nil {
		return fmt.Errorf("loading target pull request ready effect: %w", err)
	}
	if found {
		return nil
	}
	if strings.TrimSpace(in.Step.PullRequestNodeID) == "" {
		return fail(ctx, "marking the target pull request ready", fmt.Errorf("pull request node ID is empty: %w", work.ErrPermanent))
	}
	if err := a.deps.GitHub.MarkPullRequestReadyForReview(ctx, in.Step.PullRequestNodeID); err != nil {
		return fail(ctx, "marking the target pull request ready", err)
	}
	return a.checkpointRepositoryResult(ctx, cp, in.Step, repositoryEffectReady, struct{}{})
}

// TargetMergePullRequestInput binds merge to the exact reviewed candidate.
type TargetMergePullRequestInput struct {
	Step            RepositoryStep
	ExpectedHeadSHA string
}

// TargetMergePullRequest requests an exact-head merge and checkpoints GitHub's result.
func (a *RunWorkerActivities) TargetMergePullRequest(ctx context.Context, in TargetMergePullRequestInput) (work.PullRequestMergeResult, error) {
	cp, raw, found, err := a.loadRepositoryResult(ctx, in.Step, repositoryEffectMerge)
	if err != nil {
		return work.PullRequestMergeResult{}, fmt.Errorf("loading target pull request merge effect: %w", err)
	}
	if found {
		return decodeRepositoryResult[work.PullRequestMergeResult](raw)
	}
	if in.Step.PullRequestNumber <= 0 || strings.TrimSpace(in.ExpectedHeadSHA) == "" || in.ExpectedHeadSHA != in.Step.PushedHead {
		return work.PullRequestMergeResult{}, fail(ctx, "merging the target pull request", fmt.Errorf("pull request and exact current head SHA are required: %w", work.ErrPermanent))
	}
	result, err := a.deps.GitHub.MergePullRequest(ctx, in.Step.PullRequestNumber, in.ExpectedHeadSHA)
	if err != nil {
		return work.PullRequestMergeResult{}, fail(ctx, "merging the target pull request", err)
	}
	switch result.Outcome {
	case work.PullRequestMergeRetryableAmbiguity:
		diagnostic := strings.TrimSpace(result.Diagnostic)
		if diagnostic == "" {
			diagnostic = "GitHub has not resolved the authoritative merge state"
		}
		return work.PullRequestMergeResult{}, fail(ctx, "merging the target pull request", fmt.Errorf("merge result remains ambiguous: %s", diagnostic))
	case work.PullRequestMergeConfirmed:
		// A confirmed merge must remain recoverable until
		// FinalizeConfirmedMerge transactionally completes the merge Step
		// alongside the Run and Ticket.
		err = a.checkpointRepositoryEffect(ctx, cp, in.Step, repositoryEffectMerge, result)
	case work.PullRequestMergeClosedUnmerged,
		work.PullRequestMergeTextConflict,
		work.PullRequestMergeHeadChanged,
		work.PullRequestMergeBaseRefreshRequired:
		// These outcomes are terminal for this merge attempt and authorize
		// the workflow's next semantic decision, so they complete the Step.
		err = a.checkpointRepositoryResult(ctx, cp, in.Step, repositoryEffectMerge, result)
	default:
		return work.PullRequestMergeResult{}, fail(ctx, "merging the target pull request", fmt.Errorf("GitHub returned an unrecognized merge outcome: %w", work.ErrPermanent))
	}
	if err != nil {
		return work.PullRequestMergeResult{}, fmt.Errorf("checkpointing target pull request merge result: %w", err)
	}
	return result, nil
}

func (a *RunWorkerActivities) checkpointRepositoryEffect(ctx context.Context, cp RepositoryCheckpoint, position RepositoryStep, kind string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fail(ctx, "encoding repository effect result", err)
	}
	envelope, err := json.Marshal(repositoryEffectEnvelope{Kind: kind, Result: raw})
	if err != nil {
		return fail(ctx, "encoding repository effect checkpoint", err)
	}
	_, err = cp.CheckpointEffect(ctx, store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{
		RunID: a.deps.Identity.RunID, StepOrdinal: position.StepOrdinal, Branch: position.Branch,
		PushedHead: position.PushedHead, ObservedBase: position.ObservedBase,
		PullRequestNumber: position.PullRequestNumber, PullRequestNodeID: position.PullRequestNodeID, StepResult: envelope,
	}, CompletedAt: a.deps.Clock.Now().UTC()})
	if err != nil {
		return fail(ctx, fmt.Sprintf("checkpointing repository effect %d", position.StepOrdinal), err)
	}
	return nil
}

func (a *RunWorkerActivities) loadRepositoryResult(ctx context.Context, requested RepositoryStep, kind string) (RepositoryCheckpoint, json.RawMessage, bool, error) {
	if err := a.validateRepositoryBranch(ctx, requested.Branch); err != nil {
		return nil, nil, false, err
	}
	if requested.StepOrdinal <= 0 || strings.TrimSpace(requested.Branch) == "" {
		return nil, nil, false, fail(ctx, "validating repository Step", fmt.Errorf("positive ordinal and branch are required: %w", work.ErrPermanent))
	}
	cp, err := a.deps.RepositoryCheckpoints(a.deps.Identity)
	if err != nil {
		return nil, nil, false, fail(ctx, "opening repository checkpoint", err)
	}
	stored, found, err := cp.Load(ctx)
	if err != nil {
		return nil, nil, false, fail(ctx, "loading repository checkpoint", err)
	}
	if !found || stored.StepOrdinal < requested.StepOrdinal {
		return cp, nil, false, nil
	}
	if stored.StepOrdinal > requested.StepOrdinal || !repositoryPositionMatches(stored, requested) {
		return nil, nil, false, fail(ctx, "reconciling repository checkpoint", fmt.Errorf("checkpoint does not belong to the requested Step/effect: %w", work.ErrPermanent))
	}
	var envelope repositoryEffectEnvelope
	if err := json.Unmarshal(stored.StepResult, &envelope); err != nil || envelope.Kind != kind || len(envelope.Result) == 0 {
		return nil, nil, false, fail(ctx, "reconciling repository checkpoint", fmt.Errorf("durable result does not encode %s: %w", kind, work.ErrPermanent))
	}
	return cp, envelope.Result, true, nil
}

func (a *RunWorkerActivities) validateRepositoryBranch(ctx context.Context, branch string) error {
	if branch != a.deps.Branch {
		return fail(ctx, "validating repository Step", fmt.Errorf("repository Step branch does not match the Run Worker branch: %w", work.ErrPermanent))
	}
	return nil
}

func (a *RunWorkerActivities) checkpointRepositoryResult(ctx context.Context, cp RepositoryCheckpoint, position RepositoryStep, kind string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fail(ctx, "encoding repository Step result", err)
	}
	envelope, err := json.Marshal(repositoryEffectEnvelope{Kind: kind, Result: raw})
	if err != nil {
		return fail(ctx, "encoding repository checkpoint", err)
	}
	_, err = cp.Checkpoint(ctx, store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{
		RunID: a.deps.Identity.RunID, StepOrdinal: position.StepOrdinal, Branch: position.Branch,
		PushedHead: position.PushedHead, ObservedBase: position.ObservedBase,
		PullRequestNumber: position.PullRequestNumber, PullRequestNodeID: position.PullRequestNodeID, StepResult: envelope,
	}, CompletedAt: a.deps.Clock.Now().UTC()})
	if err != nil {
		return fail(ctx, fmt.Sprintf("checkpointing repository Step %d", position.StepOrdinal), err)
	}
	return nil
}

func repositoryPositionMatches(stored store.GitCheckpoint, requested RepositoryStep) bool {
	if stored.Branch != requested.Branch {
		return false
	}
	return (requested.PushedHead == "" || stored.PushedHead == requested.PushedHead) &&
		(requested.ObservedBase == "" || stored.ObservedBase == requested.ObservedBase) &&
		(requested.PullRequestNumber == 0 || stored.PullRequestNumber == requested.PullRequestNumber) &&
		(requested.PullRequestNodeID == "" || stored.PullRequestNodeID == requested.PullRequestNodeID)
}

func decodeRepositoryResult[T any](raw json.RawMessage) (T, error) {
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("decoding durable repository activity result: %w", err)
	}
	return result, nil
}
