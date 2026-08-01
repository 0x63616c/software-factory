package work

// RunOutcome is the closed business outcome of a target Run.
type RunOutcome string

const (
	// RunOutcomeSucceeded records a Confirmed Merge.
	RunOutcomeSucceeded RunOutcome = "succeeded"
	// RunOutcomeCanceled records an interrupted Run whose Ticket returned to open.
	RunOutcomeCanceled RunOutcome = "canceled"
	// RunOutcomeFailed records unrecoverable infrastructure or input failure.
	RunOutcomeFailed RunOutcome = "failed"
)

// RunFailureKind classifies a target Run failure without making raw errors control flow.
type RunFailureKind string

const (
	// RunFailureNone records a terminal outcome without a failure classification.
	RunFailureNone RunFailureKind = ""
	// RunFailureInvalidInput records an invalid target Run input.
	RunFailureInvalidInput RunFailureKind = "invalid_input"
	// RunFailureAgentUnrecoverable records an agent execution that cannot resume.
	RunFailureAgentUnrecoverable RunFailureKind = "agent_unrecoverable"
	// RunFailureCIUnobserved records an unresolved CI observation deadline.
	RunFailureCIUnobserved RunFailureKind = "ci_unobserved"
	// RunFailureGitHubAuth records an unrecoverable GitHub authentication failure.
	RunFailureGitHubAuth RunFailureKind = "github_auth"
	// RunFailureGitHubRuleset records a GitHub ruleset rejection.
	RunFailureGitHubRuleset RunFailureKind = "github_ruleset"
	// RunFailureGitHubUnavailable records an exhausted GitHub availability retry.
	RunFailureGitHubUnavailable RunFailureKind = "github_unavailable"
	// RunFailureRunWorkerUnavailable records unavailable Run Worker capacity.
	RunFailureRunWorkerUnavailable RunFailureKind = "run_worker_unavailable"
	// RunFailurePersistenceUnavailable records exhausted durable recording retries.
	RunFailurePersistenceUnavailable RunFailureKind = "persistence_unavailable"
	// RunFailureSemanticDeadline records work stopped before the hard deadline's finalization reserve.
	RunFailureSemanticDeadline RunFailureKind = "semantic_deadline"
	// RunFailureInfrastructure records another classified infrastructure failure.
	RunFailureInfrastructure RunFailureKind = "infrastructure"
)

// StepKind identifies one executor-neutral target operation.
type StepKind string

const (
	// StepCreateRunWorker creates the Run's execution worker.
	StepCreateRunWorker StepKind = "create_run_worker"
	// StepAcquireRunWorkerSession creates the worker-affine Temporal Session.
	StepAcquireRunWorkerSession StepKind = "acquire_run_worker_session"
	// StepCloneRepository clones the Run-owned repository workspace.
	StepCloneRepository StepKind = "clone_repository"
	// StepPlan performs the agent planning operation.
	StepPlan StepKind = "plan"
	// StepImplement performs one agent implementation operation.
	StepImplement StepKind = "implement"
	// StepSyncPullRequest synchronizes authoritative GitHub PR state.
	StepSyncPullRequest StepKind = "sync_pull_request"
	// StepAwaitCI observes configured checks for one exact head.
	StepAwaitCI StepKind = "await_ci"
	// StepReview performs an independent agent review.
	StepReview StepKind = "review"
	// StepMarkPullRequestReady removes draft state without requesting review.
	StepMarkPullRequestReady StepKind = "mark_pull_request_ready"
	// StepMergePullRequest asks GitHub to merge an authorized head.
	StepMergePullRequest StepKind = "merge_pull_request"
)

// StepState is a target Step lifecycle state.
type StepState string

const (
	// StepStateRunning records an active primary operation.
	StepStateRunning StepState = "running"
	// StepStateCompleted records a Step with an authoritative Result.
	StepStateCompleted StepState = "completed"
	// StepStateFailed records exhausted or non-retryable execution failure.
	StepStateFailed StepState = "failed"
)

// AgentStage is the agent-only vocabulary retained from the legacy Stage.
type AgentStage string

const (
	// AgentStagePlan is the planning agent role.
	AgentStagePlan AgentStage = "plan"
	// AgentStageImplement is the code-changing agent role.
	AgentStageImplement AgentStage = "implement"
	// AgentStageReview is the independent reviewer role.
	AgentStageReview AgentStage = "review"
)

// MatchesStep reports whether kind is the agent-backed Step for this stage.
func (s AgentStage) MatchesStep(kind StepKind) bool {
	switch s {
	case AgentStagePlan:
		return kind == StepPlan
	case AgentStageImplement:
		return kind == StepImplement
	case AgentStageReview:
		return kind == StepReview
	default:
		return false
	}
}

// AgentAttemptState records one workflow-authorized agent execution.
type AgentAttemptState string

const (
	// AgentAttemptRunning records an authorized execution in progress.
	AgentAttemptRunning AgentAttemptState = "running"
	// AgentAttemptSucceeded records an agent execution that reached terminal success.
	AgentAttemptSucceeded AgentAttemptState = "succeeded"
	// AgentAttemptFailed records an agent execution that cannot continue.
	AgentAttemptFailed AgentAttemptState = "failed"
)

// UsageState distinguishes known zero usage from unavailable usage.
type UsageState string

const (
	// UsageUnknown records unavailable provider usage rather than zero spend.
	UsageUnknown UsageState = "unknown"
	// UsageMeasured records usage captured from the provider terminal envelope.
	UsageMeasured UsageState = "measured"
)

// PullRequestState is GitHub's authoritative lifecycle state for a pull request.
type PullRequestState string

const (
	// PullRequestStateOpen records a pull request GitHub still accepts changes to.
	PullRequestStateOpen PullRequestState = "open"
	// PullRequestStateClosed records a pull request GitHub no longer accepts changes to.
	PullRequestStateClosed PullRequestState = "closed"
)

// PullRequestMergeability is GitHub's current mergeability assessment.
type PullRequestMergeability string

const (
	// PullRequestMergeabilityUnknown records an assessment GitHub is still computing.
	PullRequestMergeabilityUnknown PullRequestMergeability = "unknown"
	// PullRequestMergeabilityMergeable records a branch GitHub can merge.
	PullRequestMergeabilityMergeable PullRequestMergeability = "mergeable"
	// PullRequestMergeabilityConflicting records a textual Git conflict.
	PullRequestMergeabilityConflicting PullRequestMergeability = "conflicting"
)

// PullRequestMergeOutcome is the result of asking GitHub to merge one reviewed head.
type PullRequestMergeOutcome string

const (
	// PullRequestMergeConfirmed records GitHub's authoritative confirmation and merge SHA.
	PullRequestMergeConfirmed PullRequestMergeOutcome = "confirmed"
	// PullRequestMergeClosedUnmerged records a pull request closed without a confirmed merge.
	PullRequestMergeClosedUnmerged PullRequestMergeOutcome = "closed_unmerged"
	// PullRequestMergeTextConflict records a textual conflict requiring implementation work.
	PullRequestMergeTextConflict PullRequestMergeOutcome = "text_conflict"
	// PullRequestMergeHeadChanged records a head SHA different from the reviewed SHA.
	PullRequestMergeHeadChanged PullRequestMergeOutcome = "head_changed"
	// PullRequestMergeBaseRefreshRequired records policy requiring a refreshed base.
	PullRequestMergeBaseRefreshRequired PullRequestMergeOutcome = "base_refresh_required"
	// PullRequestMergeRetryableAmbiguity records an inconclusive GitHub answer.
	PullRequestMergeRetryableAmbiguity PullRequestMergeOutcome = "retryable_ambiguity"
)

// PullRequestMergeResult is the typed result of an exact-head merge request.
type PullRequestMergeResult struct {
	Outcome     PullRequestMergeOutcome
	MergeSHA    string
	PullRequest PullRequest
	Diagnostic  string
}

// PullRequestRetirement is the authoritative state observed while fencing a
// canceled Run's pull request before a successor starts.
type PullRequestRetirement struct {
	Merged       bool
	ReviewedHead string
	MergeSHA     string
}
