package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveCheckFailsClosedWhenSourceCommitIsMissing(t *testing.T) {
	t.Parallel()

	repo := gitOutput(t, "rev-parse", "--show-toplevel")
	head := gitOutput(t, "rev-parse", "HEAD")
	shallow := filepath.Join(t.TempDir(), "shallow")

	runGit(t, "init", shallow)
	runGit(t, "-C", shallow, "remote", "add", "origin", "file://"+repo)
	runGit(t, "-C", shallow, "fetch", "--depth=1", "origin", head)
	runGit(t, "-C", shallow, "checkout", "--detach", "FETCH_HEAD")
	script, err := os.ReadFile(filepath.Join(repo, "scripts", "archive-check.sh"))
	if err != nil {
		t.Fatalf("read current archive check: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shallow, "scripts", "archive-check.sh"), script, 0o755); err != nil {
		t.Fatalf("install current archive check in shallow fixture: %v", err)
	}

	command := exec.Command("bash", "scripts/archive-check.sh")
	command.Dir = shallow
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("archive check succeeded without its source commit:\n%s", output)
	}
	if !strings.Contains(string(output), "source commit is unavailable") {
		t.Fatalf("archive check did not explain the missing source commit:\n%s", output)
	}
}

func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	_ = gitOutput(t, args...)
}
