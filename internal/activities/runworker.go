package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// TargetRepository owns the Run Worker's credentialed repository checkout.
type TargetRepository interface {
	Prepare(context.Context, string, string) (string, error)
	PrepareFromCommit(context.Context, string, string, string) (string, error)
	Publish(context.Context, string) (string, error)
}

// TargetGitHub is the repository-scoped external surface hosted by a Run Worker.
type TargetGitHub interface {
	PullRequestForBranch(context.Context, string) (work.PullRequest, bool, error)
	OpenOrUpdatePullRequest(context.Context, string, string, string, *work.PullRequest) (work.PullRequest, error)
	MarkPullRequestReadyForReview(context.Context, string) error
	MergePullRequest(context.Context, int, string) (work.PullRequestMergeResult, error)
	ChecksForCommit(context.Context, string, []string) ([]work.CheckRun, error)
	RetirePullRequest(context.Context, int) (work.PullRequestRetirement, error)
}

// RepositoryCheckpoint is the generation-scoped recovery boundary for repository steps.
type RepositoryCheckpoint interface {
	Load(context.Context) (store.GitCheckpoint, bool, error)
	Checkpoint(context.Context, store.GitCheckpointInput) (store.GitCheckpoint, error)
	CheckpointEffect(context.Context, store.GitCheckpointInput) (store.GitCheckpoint, error)
}

// RunWorkerDeps are the credentialed repository dependencies of one Run Worker.
type RunWorkerDeps struct {
	Clock                 interface{ Now() time.Time }
	Repository            TargetRepository
	GitHub                TargetGitHub
	Identity              work.RunWorkerIdentity
	Branch                string
	RepositoryCheckpoints func(work.RunWorkerIdentity) (RepositoryCheckpoint, error)
}

// RunWorkerActivities holds only workflow-selected, credentialed repository effects.
// Model-selected tools run in the separate credential-free tools worker.
type RunWorkerActivities struct{ deps RunWorkerDeps }

// NewRunWorkerActivities constructs repository activities for one generation.
func NewRunWorkerActivities(deps RunWorkerDeps) (*RunWorkerActivities, error) {
	if deps.Clock == nil || deps.Repository == nil || deps.GitHub == nil || deps.RepositoryCheckpoints == nil {
		return nil, fmt.Errorf("run worker activities require clock, repository, GitHub, and repository checkpoints")
	}
	if err := deps.Identity.Validate(); err != nil {
		return nil, fmt.Errorf("run worker activities require a valid identity: %w", err)
	}
	if !work.FactoryTicketBranchBelongsToRun(deps.Branch, deps.Identity.RunID) {
		return nil, fmt.Errorf("run worker activities require a branch bound to Run %q", deps.Identity.RunID)
	}
	return &RunWorkerActivities{deps: deps}, nil
}
