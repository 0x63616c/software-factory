package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseVersionChoosesVersionAndCreatesAnnotatedTagFromCodexDecision(t *testing.T) {
	t.Parallel()

	sourceRepo := gitOutput(t, "rev-parse", "--show-toplevel")
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o700); err != nil {
		t.Fatalf("create scripts directory: %v", err)
	}
	for _, name := range []string{"release-prompt.md", "release-decision.schema.json"} {
		contents, err := os.ReadFile(filepath.Join(sourceRepo, "scripts", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(repo, "scripts", name), contents, 0o600); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", origin)
	runGit(t, "init", repo)
	runGit(t, "-C", repo, "config", "user.name", "Release Test")
	runGit(t, "-C", repo, "config", "user.email", "release-test@example.com")
	runGit(t, "-C", repo, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("/.artifacts/\n"), 0o600); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, "-C", repo, "add", "README.md", ".gitignore", "scripts")
	runGit(t, "-C", repo, "commit", "-m", "chore: initial release")
	runGit(t, "-C", repo, "tag", "-a", "v0.1.0", "-m", "initial release")
	runGit(t, "-C", repo, "push", "origin", "HEAD", "--tags")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("feature\n"), 0o600); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	runGit(t, "-C", repo, "add", "README.md")
	runGit(t, "-C", repo, "commit", "-m", "feat: add autonomous releases")

	binDir := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	writeExecutable(t, filepath.Join(binDir, "gh"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"repos/owner/repo/releases"* ]]; then
  printf '%s\n' '[{"tag_name":"v0.1.0"}]'
  exit 0
fi
printf 'unexpected gh invocation: %s\n' "$*" >&2
exit 1
`)
	writeExecutable(t, filepath.Join(binDir, "codex"), `#!/usr/bin/env bash
set -euo pipefail
output_file=""
while (($#)); do
  case "$1" in
    --output-last-message)
      output_file="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
tee "$RELEASE_PROMPT_FILE" >/dev/null
printf '%s\n' '{"version":"v0.2.0","releaseNotes":"# What changed\n\nAdds autonomous releases.\n\n## Changes since v0.1.0\n\n| Commit | Message | Author |\n| --- | --- | --- |\n| aaaaaaaa | feat: add autonomous releases | Release Test |"}' > "$output_file"
`)

	script := filepath.Join(sourceRepo, "scripts", "release-version.sh")
	command := exec.Command("bash", script, "--create-tag")
	command.Dir = repo
	command.Env = append(os.Environ(),
		"CODEX_BIN="+filepath.Join(binDir, "codex"),
		"GH_BIN="+filepath.Join(binDir, "gh"),
		"GITHUB_REPOSITORY=owner/repo",
		"RELEASE_PROMPT_FILE="+promptFile,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("release version: %v\n%s", err, output)
	}
	if got := gitOutput(t, "-C", repo, "tag", "--list", "v0.2.0"); got != "v0.2.0" {
		t.Fatalf("created tag = %q, want v0.2.0", got)
	}
	if got := gitOutput(t, "-C", repo, "tag", "-l", "v0.2.0", "--format=%(contents)"); got != "# What changed\n\nAdds autonomous releases.\n\n## Changes since v0.1.0\n\n| Commit | Message | Author |\n| --- | --- | --- |\n| aaaaaaaa | feat: add autonomous releases | Release Test |" {
		t.Fatalf("tag notes = %q", got)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("read codex prompt: %v", err)
	}
	for _, want := range []string{"Conventional Commits", "GitHub Release records", "feat: add autonomous releases"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt does not contain %q:\n%s", want, prompt)
		}
	}

	existingNotes := filepath.Join(t.TempDir(), "existing-release.md")
	existingTag := exec.Command("bash", script, "v0.2.0", "--no-codex", "--notes-file", existingNotes)
	existingTag.Dir = repo
	existingTag.Env = append(os.Environ(), "GITHUB_REPOSITORY=owner/repo")
	output, err = existingTag.CombinedOutput()
	if err != nil {
		t.Fatalf("generate notes for existing tag: %v\n%s", err, output)
	}
	notes, err := os.ReadFile(existingNotes)
	if err != nil {
		t.Fatalf("read existing-tag notes: %v", err)
	}
	if !strings.Contains(string(notes), "## Changes since v0.1.0") {
		t.Errorf("existing-tag notes do not use the previous tag:\n%s", notes)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
