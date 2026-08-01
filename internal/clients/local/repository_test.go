package local

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type gitCall struct {
	dir  string
	argv []string
}

type gitRunnerProbe struct {
	calls  []gitCall
	remote bool
	branch string
}

func (p *gitRunnerProbe) Run(_ context.Context, dir string, argv []string) (string, int, error) {
	p.calls = append(p.calls, gitCall{dir: dir, argv: append([]string(nil), argv...)})
	joined := strings.Join(argv, " ")
	switch {
	case strings.Contains(joined, "remote get-url"):
		return "https://github.com/example/repo.git\n", 0, nil
	case strings.Contains(joined, "rev-parse --verify --quiet"):
		if p.remote || strings.Contains(joined, "^{commit}") {
			return "remote-head\n", 0, nil
		}
		return "", 1, nil
	case strings.Contains(joined, "rev-parse HEAD"):
		return "head-sha\n", 0, nil
	case strings.Contains(joined, "symbolic-ref --quiet --short HEAD"):
		if p.branch == "" {
			return "factory/run-42\n", 0, nil
		}
		return p.branch + "\n", 0, nil
	default:
		return "", 0, nil
	}
}

func TestRepositoryFreshCloneCreatesAndPushesTheRunBranch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	runner := &gitRunnerProbe{}
	repo, err := NewRepository(root, runner)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	head, err := repo.Prepare(context.Background(), "https://github.com/example/repo.git", "factory/run-42")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if head != "head-sha" {
		t.Fatalf("head = %q", head)
	}
	want := [][]string{
		{"git", "clone", "--origin", "origin", "https://github.com/example/repo.git", root},
		{"git", "fetch", "--prune", "origin"},
		{"git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/factory/run-42"},
		{"git", "checkout", "-B", "factory/run-42"},
		{"git", "push", "--set-upstream", "origin", "factory/run-42"},
		{"git", "rev-parse", "HEAD"},
	}
	assertGitArgv(t, runner.calls, want)
}

func TestRepositoryReplacementRestoresTheLastPushedBranch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &gitRunnerProbe{remote: true}
	repo, err := NewRepository(root, runner)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if _, err := repo.Prepare(context.Background(), "https://github.com/example/repo.git", "factory/run-42"); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	want := [][]string{
		{"git", "remote", "get-url", "origin"},
		{"git", "fetch", "--prune", "origin"},
		{"git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/factory/run-42"},
		{"git", "checkout", "-B", "factory/run-42", "refs/remotes/origin/factory/run-42"},
		{"git", "rev-parse", "HEAD"},
	}
	assertGitArgv(t, runner.calls, want)
}

func TestRepositoryCarryForwardCreatesFreshBranchAtTheDurableCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	runner := &gitRunnerProbe{}
	repo, err := NewRepository(root, runner)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	commit := "0123456789abcdef0123456789abcdef01234567"
	head, err := repo.PrepareFromCommit(context.Background(), "https://github.com/example/repo.git", "factory/ticket-42/new-run", commit)
	if err != nil {
		t.Fatalf("PrepareFromCommit: %v", err)
	}
	if head != "head-sha" {
		t.Fatalf("head = %q", head)
	}
	assertGitArgv(t, runner.calls, [][]string{
		{"git", "clone", "--origin", "origin", "https://github.com/example/repo.git", root},
		{"git", "fetch", "--prune", "origin"},
		{"git", "rev-parse", "--verify", "--quiet", commit + "^{commit}"},
		{"git", "checkout", "-B", "factory/ticket-42/new-run", commit},
		{"git", "push", "--set-upstream", "origin", "factory/ticket-42/new-run"},
		{"git", "rev-parse", "HEAD"},
	})
}

func TestRepositoryPublishesTheCommittedHeadFromTheExpectedRunBranch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	runner := &gitRunnerProbe{}
	repo, err := NewRepository(root, runner)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	head, err := repo.Publish(context.Background(), "factory/run-42")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if head != "head-sha" {
		t.Fatalf("head = %q", head)
	}
	assertGitArgv(t, runner.calls, [][]string{
		{"git", "symbolic-ref", "--quiet", "--short", "HEAD"},
		{"git", "rev-parse", "HEAD"},
		{"git", "push", "origin", "HEAD:refs/heads/factory/run-42"},
	})
}

func TestRepositoryRejectsAnExistingCheckoutFromAnotherRemote(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &gitRunnerProbe{}
	repo, err := NewRepository(root, runner)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if _, err := repo.Prepare(context.Background(), "https://github.com/another/repo.git", "factory/run-42"); err == nil {
		t.Fatal("Prepare accepted a checkout from another remote")
	}
}

func TestRepositoryRejectsUnsafeInputsBeforeRunningGit(t *testing.T) {
	runner := &gitRunnerProbe{}
	if _, err := NewRepository("relative/repository", runner); err == nil {
		t.Fatal("NewRepository accepted a relative root")
	}
	repo, err := NewRepository(filepath.Join(t.TempDir(), "repository"), runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range [][2]string{{"", "factory/run"}, {"https://github.com/example/repo.git", ""}, {"https://github.com/example/repo.git", "--upload-pack=bad"}} {
		if _, err := repo.Prepare(context.Background(), input[0], input[1]); err == nil {
			t.Fatalf("Prepare accepted url=%q branch=%q", input[0], input[1])
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unsafe inputs reached git: %+v", runner.calls)
	}
}

func TestOSGitRunnerSeparatesExitEvidenceFromExecutionFailure(t *testing.T) {
	runner := OSGitRunner{}
	if _, code, err := runner.Run(context.Background(), t.TempDir(), []string{"git", "rev-parse", "--verify", "missing"}); err != nil || code == 0 {
		t.Fatalf("ordinary git failure = code %d, err %v", code, err)
	}
	if _, _, err := runner.Run(context.Background(), t.TempDir(), []string{"definitely-not-a-command"}); err == nil {
		t.Fatal("missing executable was reported as ordinary exit evidence")
	}
}

func assertGitArgv(t *testing.T, got []gitCall, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %+v, want %d", got, len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i].argv, want[i]) {
			t.Errorf("call %d = %#v, want %#v", i, got[i].argv, want[i])
		}
		for _, arg := range got[i].argv {
			if strings.Contains(arg, "secret") {
				t.Fatalf("call argv contains a credential: %#v", got[i].argv)
			}
		}
	}
}
