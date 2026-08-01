package activities

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// RunWorkerLifecycle is the main worker's target-only Kubernetes authority.
// Credentials enter these methods in-process and never cross Temporal.
type RunWorkerLifecycle interface {
	Provision(context.Context, work.RunWorkerSpec, work.RunWorkerSecretMaterial) (work.RunWorkerID, error)
	RotateGitHubCredential(context.Context, work.RunWorkerIdentity, work.Credential, string, time.Time) (work.RunWorkerCredentialRevision, error)
	InstallCheckpointCapability(context.Context, work.RunWorkerIdentity, store.TargetAttemptID, work.Credential) (work.Credential, error)
	InstallRepositoryCapability(context.Context, work.RunWorkerIdentity, work.Credential) (work.Credential, error)
	Delete(context.Context, work.RunWorkerIdentity) error
}

// RunWorkerInventory lists only public Run and generation identities. It is a
// separate capability from lifecycle mutation because maintenance needs an
// inventory without ever reading projected Secret data.
type RunWorkerInventory interface {
	List(context.Context) ([]work.RunWorkerIdentity, error)
}

// GitHubCredentialSource mints one short-lived repository credential inside a
// main-control activity. The credential must never be returned by that method.
type GitHubCredentialSource interface {
	InstallationToken(context.Context) (work.GitHubCredential, error)
}

// CheckpointCapabilityMinter creates an opaque capability inside the activity.
type CheckpointCapabilityMinter interface {
	Mint() (work.Credential, error)
}

// CheckpointCapabilityBinder binds an opaque value to one exact active Agent
// Attempt after Kubernetes has made that same value authoritative.
type CheckpointCapabilityBinder interface {
	BindCheckpointCapability(context.Context, store.TargetAttemptID, string) error
}

// RepositoryCapabilityBinder fences Git/PR checkpoint writes to the active
// Run Worker generation.
type RepositoryCapabilityBinder interface {
	BindRepositoryCapability(context.Context, work.RunWorkerIdentity, string) error
}

// RunWorkerTemplate is deployment-owned worker shape, not workflow input.
type RunWorkerTemplate struct {
	Image           string
	CPURequest      string
	MemoryLimit     string
	DeadlineSeconds int64
	Env             map[string]string
}

// RunWorkerControlDeps are capabilities that remain on the main queue.
type RunWorkerControlDeps struct {
	Workers          RunWorkerLifecycle
	GitHub           GitHubCredentialSource
	Capabilities     CheckpointCapabilityMinter
	Binder           CheckpointCapabilityBinder
	RepositoryBinder RepositoryCapabilityBinder
	Template         RunWorkerTemplate
}

// RunWorkerControlActivities provisions and renews Run Workers without ever
// admitting credentials to workflow input, output, logs, or history.
type RunWorkerControlActivities struct{ deps RunWorkerControlDeps }

// NewRunWorkerControlActivities validates and seals the main-control dependencies.
func NewRunWorkerControlActivities(deps RunWorkerControlDeps) (*RunWorkerControlActivities, error) {
	missing := []string{}
	if deps.Workers == nil {
		missing = append(missing, "Workers")
	}
	if deps.GitHub == nil {
		missing = append(missing, "GitHub")
	}
	if deps.Capabilities == nil {
		missing = append(missing, "Capabilities")
	}
	if deps.Binder == nil {
		missing = append(missing, "Binder")
	}
	if deps.RepositoryBinder == nil {
		missing = append(missing, "RepositoryBinder")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("run worker control activities require %v", missing)
	}
	if strings.TrimSpace(deps.Template.Image) == "" || strings.TrimSpace(deps.Template.CPURequest) == "" || strings.TrimSpace(deps.Template.MemoryLimit) == "" || deps.Template.DeadlineSeconds <= 0 {
		return nil, fmt.Errorf("run worker control activities require a complete worker template")
	}
	return &RunWorkerControlActivities{deps: deps}, nil
}

// ProvisionRunWorkerInput names the deterministic worker generation to create.
type ProvisionRunWorkerInput struct {
	TicketNumber int
	Identity     work.RunWorkerIdentity
	Branch       string
}

// ProvisionRunWorkerOutput contains only the safe deterministic worker identity.
type ProvisionRunWorkerOutput struct {
	ID work.RunWorkerID
}

// ProvisionRunWorker fetches every credential inside the activity, projects
// both checkpoint capabilities, and returns only the deterministic worker identity.
func (a *RunWorkerControlActivities) ProvisionRunWorker(ctx context.Context, in ProvisionRunWorkerInput) (ProvisionRunWorkerOutput, error) {
	if in.TicketNumber <= 0 || strings.TrimSpace(in.Branch) == "" {
		return ProvisionRunWorkerOutput{}, fail(ctx, "validating Run Worker provisioning", fmt.Errorf("ticket number must be positive: %w", work.ErrPermanent))
	}
	if err := in.Identity.Validate(); err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "validating Run Worker provisioning", err)
	}
	githubCredential, err := a.deps.GitHub.InstallationToken(ctx)
	if err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "minting the Run Worker GitHub credential", err)
	}
	bootstrapCapability, err := a.deps.Capabilities.Mint()
	if err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "minting the Run Worker bootstrap capability", err)
	}
	env := cloneRunWorkerEnv(a.deps.Template.Env)
	env[work.RunWorkerBranchEnv] = in.Branch
	spec, err := work.NewRunWorkerSpec(work.RunWorkerSpec{
		TicketNumber: in.TicketNumber, Identity: in.Identity,
		Image: a.deps.Template.Image, CPURequest: a.deps.Template.CPURequest, MemoryLimit: a.deps.Template.MemoryLimit,
		DeadlineSeconds: a.deps.Template.DeadlineSeconds, Env: env,
	})
	if err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "constructing the Run Worker specification", err)
	}
	id, err := a.deps.Workers.Provision(ctx, spec, work.RunWorkerSecretMaterial{
		GitHubToken: githubCredential.Token, GitHubLogin: githubCredential.Login,
		GitHubExpiresAt: githubCredential.ExpiresAt, CheckpointCapability: bootstrapCapability,
	})
	if err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "provisioning the Run Worker", err)
	}
	repositoryCapability, err := a.deps.Capabilities.Mint()
	if err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "minting the Run Worker repository capability", err)
	}
	installedRepositoryCapability, err := a.deps.Workers.InstallRepositoryCapability(ctx, in.Identity, repositoryCapability)
	if err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "installing the Run Worker repository capability", err)
	}
	if err := a.deps.RepositoryBinder.BindRepositoryCapability(ctx, in.Identity, installedRepositoryCapability.Reveal()); err != nil {
		return ProvisionRunWorkerOutput{}, fail(ctx, "binding the Run Worker repository capability", err)
	}
	return ProvisionRunWorkerOutput{ID: id}, nil
}

// RotateRunWorkerGitHubCredentialInput names the generation receiving a new token.
type RotateRunWorkerGitHubCredentialInput struct {
	Identity work.RunWorkerIdentity
}

// RotateRunWorkerGitHubCredential combines mint and install so a token never
// appears in either side of the Temporal activity boundary.
func (a *RunWorkerControlActivities) RotateRunWorkerGitHubCredential(ctx context.Context, in RotateRunWorkerGitHubCredentialInput) (work.RunWorkerCredentialRevision, error) {
	credential, err := a.deps.GitHub.InstallationToken(ctx)
	if err != nil {
		return work.RunWorkerCredentialRevision{}, fail(ctx, "minting the Run Worker GitHub credential", err)
	}
	revision, err := a.deps.Workers.RotateGitHubCredential(ctx, in.Identity, credential.Token, credential.Login, credential.ExpiresAt)
	if err != nil {
		return work.RunWorkerCredentialRevision{}, fail(ctx, "installing the Run Worker GitHub credential", err)
	}
	if err := revision.Validate(); err != nil {
		return work.RunWorkerCredentialRevision{}, fail(ctx, "validating the installed Run Worker GitHub credential", err)
	}
	return revision, nil
}

// AuthorizeRunWorkerAttemptInput binds one exact Agent Attempt to its projected capability.
type AuthorizeRunWorkerAttemptInput struct {
	Identity  work.RunWorkerIdentity
	AttemptID store.TargetAttemptID
}

// AuthorizeRunWorkerAttempt installs first, then binds the value Kubernetes
// reports as authoritative. An exact retry therefore binds the already
// projected capability after a lost activity response instead of rotating it.
func (a *RunWorkerControlActivities) AuthorizeRunWorkerAttempt(ctx context.Context, in AuthorizeRunWorkerAttemptInput) error {
	proposed, err := a.deps.Capabilities.Mint()
	if err != nil {
		return fail(ctx, fmt.Sprintf("minting checkpoint capability for %s", in.AttemptID), err)
	}
	projected, err := a.deps.Workers.InstallCheckpointCapability(ctx, in.Identity, in.AttemptID, proposed)
	if err != nil {
		return fail(ctx, fmt.Sprintf("installing checkpoint capability for %s", in.AttemptID), err)
	}
	if err := a.deps.Binder.BindCheckpointCapability(ctx, in.AttemptID, projected.Reveal()); err != nil {
		return fail(ctx, fmt.Sprintf("binding checkpoint capability for %s", in.AttemptID), err)
	}
	return nil
}

// DeleteRunWorkerInput names the generation whose temporary resources should be removed.
type DeleteRunWorkerInput struct {
	Identity work.RunWorkerIdentity
}

// DeleteRunWorker idempotently removes one generation's temporary resources.
func (a *RunWorkerControlActivities) DeleteRunWorker(ctx context.Context, in DeleteRunWorkerInput) error {
	if err := a.deps.Workers.Delete(ctx, in.Identity); err != nil {
		return fail(ctx, "deleting the Run Worker", err)
	}
	return nil
}

// ListRunWorkers exposes the non-secret Kubernetes worker inventory to the
// finite maintenance workflow. Normal run control does not require inventory,
// so deployments without it fail explicitly instead of reporting no workers.
func (a *RunWorkerControlActivities) ListRunWorkers(ctx context.Context) ([]work.RunWorkerIdentity, error) {
	inventory, ok := a.deps.Workers.(RunWorkerInventory)
	if !ok {
		return nil, fail(ctx, "listing Run Workers", fmt.Errorf("run-worker lifecycle does not expose inventory: %w", work.ErrPermanent))
	}
	workers, err := inventory.List(ctx)
	if err != nil {
		return nil, fail(ctx, "listing Run Workers", err)
	}
	return workers, nil
}

func cloneRunWorkerEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
