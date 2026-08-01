// Package local implements repository operations inside one generation-local
// Run Worker checkout.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/0x63616c/software-factory/internal/work"
)

// GitRunner is the process edge Repository needs. argv is always passed
// directly to exec, never joined into a shell command.
type GitRunner interface {
	Run(context.Context, string, []string) (stdout string, exitCode int, err error)
}

// OSGitRunner runs Git as a local child process in the supplied checkout.
type OSGitRunner struct{}

// Run executes argv directly inside dir and reports process output and status.
func (OSGitRunner) Run(ctx context.Context, dir string, argv []string) (string, int, error) {
	if len(argv) == 0 {
		return "", 0, fmt.Errorf("running local git command: argv is empty: %w", work.ErrPermanent)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), 0, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", 0, fmt.Errorf("running %s: %w", argv[0], ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), exitErr.ExitCode(), nil
	}
	return "", 0, fmt.Errorf("running %s: %w", argv[0], err)
}

// Repository owns the one checkout on a Run Worker's writable filesystem.
type Repository struct {
	root   string
	runner GitRunner
}

// NewRepository constructs one local checkout rooted at a specific directory.
func NewRepository(root string, runner GitRunner) (*Repository, error) {
	clean := filepath.Clean(root)
	if !filepath.IsAbs(root) || clean == string(filepath.Separator) {
		return nil, fmt.Errorf("constructing local repository: root %q must be a specific absolute path", root)
	}
	if runner == nil {
		return nil, fmt.Errorf("constructing local repository: git runner is nil")
	}
	return &Repository{root: clean, runner: runner}, nil
}

// Prepare clones a missing checkout or restores an existing/replacement
// checkout to the last head GitHub accepted for this Run's branch.
func (r *Repository) Prepare(ctx context.Context, cloneURL, branch string) (string, error) {
	if err := validateRepositoryInput(cloneURL, branch); err != nil {
		return "", fmt.Errorf("validating target repository inputs: %w", err)
	}
	if err := r.prepareCheckout(ctx, cloneURL); err != nil {
		return "", fmt.Errorf("preparing target checkout: %w", err)
	}
	remoteRef := "refs/remotes/origin/" + branch
	_, remoteCode, err := r.runner.Run(ctx, r.root, []string{"git", "rev-parse", "--verify", "--quiet", remoteRef})
	if err != nil {
		return "", fmt.Errorf("checking the target branch: %w", err)
	}
	switch remoteCode {
	case 0:
		if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "checkout", "-B", branch, remoteRef}); err != nil || code != 0 {
			return "", commandFailure("restoring the target branch", code, err)
		}
	case 1:
		if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "checkout", "-B", branch}); err != nil || code != 0 {
			return "", commandFailure("creating the target branch", code, err)
		}
		if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "push", "--set-upstream", "origin", branch}); err != nil || code != 0 {
			return "", commandFailure("publishing the target branch", code, err)
		}
	default:
		return "", fmt.Errorf("checking the target branch exited %d", remoteCode)
	}
	return r.currentHead(ctx)
}

// PrepareFromCommit creates this Run's fresh branch at an exact durable commit
// from a canceled predecessor. It never relies on the mutable default branch.
func (r *Repository) PrepareFromCommit(ctx context.Context, cloneURL, branch, commit string) (string, error) {
	if err := validateRepositoryInput(cloneURL, branch); err != nil {
		return "", fmt.Errorf("validating target repository inputs: %w", err)
	}
	if !validCommitID(commit) {
		return "", fmt.Errorf("validating carry-forward commit: %w", work.ErrPermanent)
	}
	if err := r.prepareCheckout(ctx, cloneURL); err != nil {
		return "", fmt.Errorf("preparing carry-forward checkout: %w", err)
	}
	if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "rev-parse", "--verify", "--quiet", commit + "^{commit}"}); err != nil || code != 0 {
		return "", commandFailure("verifying the carry-forward commit", code, err)
	}
	if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "checkout", "-B", branch, commit}); err != nil || code != 0 {
		return "", commandFailure("creating the carry-forward branch", code, err)
	}
	if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "push", "--set-upstream", "origin", branch}); err != nil || code != 0 {
		return "", commandFailure("publishing the carry-forward branch", code, err)
	}
	return r.currentHead(ctx)
}

// Publish pushes the checkout's committed head to its Run-owned branch.
func (r *Repository) Publish(ctx context.Context, branch string) (string, error) {
	if err := validateBranch(branch); err != nil {
		return "", fmt.Errorf("validating target branch: %w", err)
	}
	currentBranch, code, err := r.runner.Run(ctx, r.root, []string{"git", "symbolic-ref", "--quiet", "--short", "HEAD"})
	if err != nil || code != 0 {
		return "", commandFailure("reading the checked out target branch", code, err)
	}
	if strings.TrimSpace(currentBranch) != branch {
		return "", fmt.Errorf("checked out branch %q does not match Run branch %q: %w", strings.TrimSpace(currentBranch), branch, work.ErrPermanent)
	}
	head, err := r.currentHead(ctx)
	if err != nil {
		return "", err
	}
	if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "push", "origin", "HEAD:refs/heads/" + branch}); err != nil || code != 0 {
		return "", commandFailure("publishing the committed target head", code, err)
	}
	return head, nil
}

func (r *Repository) prepareCheckout(ctx context.Context, cloneURL string) error {
	gitDir := filepath.Join(r.root, ".git")
	_, statErr := os.Stat(gitDir)
	switch {
	case statErr == nil:
		remote, code, err := r.runner.Run(ctx, r.root, []string{"git", "remote", "get-url", "origin"})
		if err != nil {
			return fmt.Errorf("reading the local repository remote: %w", err)
		}
		if code != 0 || strings.TrimSpace(remote) != cloneURL {
			return fmt.Errorf("the existing checkout does not belong to the configured repository: %w", work.ErrPermanent)
		}
	case errors.Is(statErr, os.ErrNotExist):
		if err := r.replacePartialCheckout(); err != nil {
			return fmt.Errorf("removing partial target repository checkout: %w", err)
		}
		if _, code, err := r.runner.Run(ctx, filepath.Dir(r.root), []string{"git", "clone", "--origin", "origin", cloneURL, r.root}); err != nil || code != 0 {
			return commandFailure("cloning the target repository", code, err)
		}
	default:
		return fmt.Errorf("checking the local repository: %w", statErr)
	}

	if _, code, err := r.runner.Run(ctx, r.root, []string{"git", "fetch", "--prune", "origin"}); err != nil || code != 0 {
		return commandFailure("fetching the target repository", code, err)
	}
	return nil
}

func (r *Repository) currentHead(ctx context.Context) (string, error) {
	head, code, err := r.runner.Run(ctx, r.root, []string{"git", "rev-parse", "HEAD"})
	if err != nil || code != 0 {
		return "", commandFailure("reading the restored target head", code, err)
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return "", fmt.Errorf("the restored target branch has no head: %w", work.ErrPermanent)
	}
	return head, nil
}

func validCommitID(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	for _, char := range commit {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func (r *Repository) replacePartialCheckout() error {
	if err := os.RemoveAll(r.root); err != nil {
		return fmt.Errorf("removing a partial target checkout at %s: %w", r.root, err)
	}
	if err := os.MkdirAll(filepath.Dir(r.root), 0o755); err != nil {
		return fmt.Errorf("creating the target checkout parent: %w", err)
	}
	return nil
}

func validateRepositoryInput(cloneURL, branch string) error {
	if !strings.HasPrefix(cloneURL, "https://github.com/") || !strings.HasSuffix(cloneURL, ".git") || strings.ContainsAny(cloneURL, "\r\n\t @") {
		return fmt.Errorf("target repository URL is not a GitHub HTTPS clone URL: %w", work.ErrPermanent)
	}
	return validateBranch(branch)
}

func validateBranch(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") {
		return fmt.Errorf("target branch %q is unsafe: %w", branch, work.ErrPermanent)
	}
	for _, char := range branch {
		if unicode.IsSpace(char) || unicode.IsControl(char) || strings.ContainsRune("~^:?*[\\", char) {
			return fmt.Errorf("target branch %q is unsafe: %w", branch, work.ErrPermanent)
		}
	}
	return nil
}

func commandFailure(action string, code int, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: git exited %d", action, code)
}
