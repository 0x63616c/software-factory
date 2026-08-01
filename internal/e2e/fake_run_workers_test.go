//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/0x63616c/software-factory/internal/activities"
	checkpointclient "github.com/0x63616c/software-factory/internal/clients/checkpoint"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

type fakeRunWorkers struct {
	t          *testing.T
	client     temporalclient.Client
	store      *store.Store
	mu         sync.Mutex
	workers    map[work.RunWorkerIdentity]worker.Worker
	capability map[work.RunWorkerIdentity]string
	revisions  atomic.Int32
}

func newFakeRunWorkers(t *testing.T, client temporalclient.Client, factoryStore *store.Store) *fakeRunWorkers {
	t.Helper()
	r := &fakeRunWorkers{
		t: t, client: client, store: factoryStore,
		workers: make(map[work.RunWorkerIdentity]worker.Worker), capability: make(map[work.RunWorkerIdentity]string),
	}
	t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for identity, temporalWorker := range r.workers {
			temporalWorker.Stop()
			delete(r.workers, identity)
		}
	})
	return r
}

func (r *fakeRunWorkers) registerMain(mainWorker worker.Worker) {
	r.t.Helper()
	minter, err := checkpointclient.NewCapabilityMinter(bytes.NewReader(bytes.Repeat([]byte{0x3c}, 32*64)))
	if err != nil {
		r.t.Fatalf("create deterministic capability minter: %v", err)
	}
	control, err := activities.NewRunWorkerControlActivities(activities.RunWorkerControlDeps{
		Workers: r, GitHub: fakeGitHubCredentialSource{}, Capabilities: minter,
		Binder: r.store, RepositoryBinder: r.store,
		Template: activities.RunWorkerTemplate{
			Image: "e2e/run-worker:local", CPURequest: "1m", MemoryLimit: "32Mi", DeadlineSeconds: 120,
			Env: map[string]string{"SOFTWARE_FACTORY_E2E": "1"},
		},
	})
	if err != nil {
		r.t.Fatalf("create Run Worker control activities: %v", err)
	}
	mainWorker.RegisterActivity(control)
}

func (r *fakeRunWorkers) Provision(_ context.Context, spec work.RunWorkerSpec, _ work.RunWorkerSecretMaterial) (work.RunWorkerID, error) {
	queue, err := work.RunWorkerTaskQueue(spec.Identity)
	if err != nil {
		return "", err
	}
	id, err := work.RunWorkerName(spec.Identity)
	if err != nil {
		return "", err
	}
	branch := spec.Env[work.RunWorkerBranchEnv]
	target, err := activities.NewRunWorkerActivities(activities.RunWorkerDeps{
		Clock: clock.System{}, Repository: fakeRepository{}, GitHub: fakeGitHub{},
		Identity: spec.Identity, Branch: branch,
		RepositoryCheckpoints: func(identity work.RunWorkerIdentity) (activities.RepositoryCheckpoint, error) {
			r.mu.Lock()
			capability := r.capability[identity]
			r.mu.Unlock()
			if capability == "" {
				return nil, fmt.Errorf("repository capability is not installed for %+v", identity)
			}
			return repositoryCheckpoint{store: r.store, identity: identity, capability: capability}, nil
		},
	})
	if err != nil {
		return "", err
	}
	temporalWorker := worker.New(r.client, queue, worker.Options{EnableSessionWorker: true, MaxConcurrentSessionExecutionSize: 1})
	temporalWorker.RegisterActivity(target)
	if err := temporalWorker.Start(); err != nil {
		return "", fmt.Errorf("start fake Run Worker: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.workers[spec.Identity]; exists {
		temporalWorker.Stop()
		return "", fmt.Errorf("Run Worker already exists for %+v", spec.Identity)
	}
	r.workers[spec.Identity] = temporalWorker
	return id, nil
}

func (r *fakeRunWorkers) RotateGitHubCredential(_ context.Context, identity work.RunWorkerIdentity, _ work.Credential, _ string, expiresAt time.Time) (work.RunWorkerCredentialRevision, error) {
	if err := identity.Validate(); err != nil {
		return work.RunWorkerCredentialRevision{}, err
	}
	return work.RunWorkerCredentialRevision{Revision: fmt.Sprintf("e2e-%d", r.revisions.Add(1)), ExpiresAt: expiresAt}, nil
}

func (r *fakeRunWorkers) InstallCheckpointCapability(_ context.Context, identity work.RunWorkerIdentity, _ store.TargetAttemptID, proposed work.Credential) (work.Credential, error) {
	if err := identity.Validate(); err != nil {
		return work.Credential{}, err
	}
	return proposed, nil
}

func (r *fakeRunWorkers) InstallRepositoryCapability(_ context.Context, identity work.RunWorkerIdentity, proposed work.Credential) (work.Credential, error) {
	if err := identity.Validate(); err != nil {
		return work.Credential{}, err
	}
	r.mu.Lock()
	r.capability[identity] = proposed.Reveal()
	r.mu.Unlock()
	return proposed, nil
}

func (r *fakeRunWorkers) Delete(_ context.Context, identity work.RunWorkerIdentity) error {
	r.mu.Lock()
	temporalWorker := r.workers[identity]
	delete(r.workers, identity)
	delete(r.capability, identity)
	r.mu.Unlock()
	if temporalWorker != nil {
		temporalWorker.Stop()
	}
	return nil
}

func (r *fakeRunWorkers) remaining() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.workers)
}

type fakeGitHubCredentialSource struct{}

func (fakeGitHubCredentialSource) InstallationToken(context.Context) (work.GitHubCredential, error) {
	return work.GitHubCredential{
		Token: work.NewCredential("e2e-github-token"), Login: "software-factory-e2e[bot]", AccountID: 1,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, nil
}

type repositoryCheckpoint struct {
	store      *store.Store
	identity   work.RunWorkerIdentity
	capability string
}

func (c repositoryCheckpoint) Load(ctx context.Context) (store.GitCheckpoint, bool, error) {
	return c.store.LoadRepositoryCheckpoint(ctx, c.identity, c.capability)
}

func (c repositoryCheckpoint) Checkpoint(ctx context.Context, in store.GitCheckpointInput) (store.GitCheckpoint, error) {
	return c.store.CheckpointRepository(ctx, store.RepositoryCheckpointInput{
		Identity: c.identity, Capability: c.capability, GitCheckpoint: in.GitCheckpoint, CompletedAt: in.CompletedAt,
	})
}

func (c repositoryCheckpoint) CheckpointEffect(ctx context.Context, in store.GitCheckpointInput) (store.GitCheckpoint, error) {
	return c.store.CheckpointRepositoryEffect(ctx, store.RepositoryCheckpointInput{
		Identity: c.identity, Capability: c.capability, GitCheckpoint: in.GitCheckpoint, CompletedAt: in.CompletedAt,
	})
}

type fakeRepository struct{}

func (fakeRepository) Prepare(context.Context, string, string) (string, error) {
	return "base-head", nil
}

func (fakeRepository) PrepareFromCommit(_ context.Context, _, _, commit string) (string, error) {
	return commit, nil
}

func (fakeRepository) Publish(context.Context, string) (string, error) {
	return "candidate-head", nil
}

type fakeGitHub struct{}

func (fakeGitHub) PullRequestForBranch(context.Context, string) (work.PullRequest, bool, error) {
	return work.PullRequest{}, false, nil
}

func (fakeGitHub) OpenOrUpdatePullRequest(_ context.Context, _, title, body string, _ *work.PullRequest) (work.PullRequest, error) {
	return work.PullRequest{
		Number: 42, URL: "https://github.invalid/example/e2e/pull/42", State: work.PullRequestStateOpen,
		HeadSHA: "candidate-head", BaseSHA: "base-head", Mergeability: work.PullRequestMergeabilityMergeable,
		Draft: true, NodeID: "PR_e2e", Title: title, Body: body,
	}, nil
}

func (fakeGitHub) MarkPullRequestReadyForReview(context.Context, string) error { return nil }

func (fakeGitHub) MergePullRequest(_ context.Context, number int, expectedHead string) (work.PullRequestMergeResult, error) {
	if number != 42 || expectedHead != "candidate-head" {
		return work.PullRequestMergeResult{}, fmt.Errorf("merge request = PR %d at %q", number, expectedHead)
	}
	return work.PullRequestMergeResult{
		Outcome: work.PullRequestMergeConfirmed, MergeSHA: "merge-head",
		PullRequest: work.PullRequest{Number: number, State: work.PullRequestStateClosed, HeadSHA: expectedHead, MergeSHA: "merge-head"},
	}, nil
}

func (fakeGitHub) ChecksForCommit(_ context.Context, commit string, required []string) ([]work.CheckRun, error) {
	if commit != "candidate-head" {
		return nil, fmt.Errorf("checks requested for %q", commit)
	}
	checks := make([]work.CheckRun, 0, len(required))
	for _, name := range required {
		checks = append(checks, work.CheckRun{Name: name, Completed: true, Conclusion: "success"})
	}
	return checks, nil
}

func (fakeGitHub) RetirePullRequest(context.Context, int) (work.PullRequestRetirement, error) {
	return work.PullRequestRetirement{}, nil
}
