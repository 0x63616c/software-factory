package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyGitHubPolicyFetchesDetailedRulesetsAndUsesDedicatedVerifier(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fakeGH := `#!/usr/bin/env bash
set -euo pipefail
args="$*"
if [[ "$args" == *"rulesets?includes_parents=true"* ]]; then
  printf '71\n72\n'
elif [[ "$args" == *"/rulesets/71"* ]]; then
  printf '%s\n' '{"name":"approval","enforcement":"active","target":"branch","conditions":{"ref_name":{"include":["refs/heads/main"],"exclude":[]}},"bypass_actors":[{"actor_id":42,"actor_type":"Integration","bypass_mode":"pull_request"}],"rules":[{"type":"pull_request","parameters":{"required_approving_review_count":1}}]}'
elif [[ "$args" == *"/rulesets/72"* ]]; then
  printf '%s\n' '{"name":"checks","enforcement":"active","target":"branch","conditions":{"ref_name":{"include":["refs/heads/main"],"exclude":[]}},"bypass_actors":[],"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"test-software-factory"}]}}]}'
else
  printf 'unexpected fake gh invocation: %s\n' "$args" >&2
  exit 90
fi
`
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(fakeGH), 0o700); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}

	command := exec.Command("bash", "scripts/verify-github-policy.sh", "--repository", "owner/repo", "--app-id", "42", "--branch", "main")
	command.Dir = ".."
	command.Env = append(os.Environ(), "GH_BIN="+path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("verify policy: %v\n%s", err, output)
	}
	var report struct {
		Ready           bool     `json:"ready"`
		ApprovalRuleset string   `json:"approvalRuleset"`
		RequiredChecks  []string `json:"requiredChecks"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, output)
	}
	if !report.Ready || report.ApprovalRuleset != "approval" || strings.Join(report.RequiredChecks, ",") != "test-software-factory" {
		t.Fatalf("report = %+v, want ready split policy", report)
	}
}
