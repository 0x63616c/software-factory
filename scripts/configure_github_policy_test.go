package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	approvalRulesetID = "20075698"
	factoryAppID      = "4399553"
	ownerUserID       = "6991398"
)

func TestConfigureGitHubPolicyDryRunIsInertAndPlansSafeOrdering(t *testing.T) {
	t.Parallel()
	fixture := newPolicyFixture(t, false)
	output, err := fixture.run(t)
	if err != nil {
		t.Fatalf("configure dry-run: %v\n%s", err, output)
	}
	if strings.TrimSpace(fixture.mutations(t)) != "" {
		t.Fatalf("dry-run mutations = %q, want none", fixture.mutations(t))
	}
	if strings.Contains(output, `"integration_id"`) {
		t.Fatalf("checks payload contains integration_id, which GitHub rejects when it is null:\n%s", output)
	}
	var report struct {
		Mode       string `json:"mode"`
		Operations []struct {
			Kind    string `json:"kind"`
			Payload struct {
				Name         string `json:"name"`
				BypassActors []struct {
					ActorID    int64  `json:"actor_id"`
					ActorType  string `json:"actor_type"`
					BypassMode string `json:"bypass_mode"`
				} `json:"bypass_actors"`
				Rules []struct {
					Type       string `json:"type"`
					Parameters struct {
						RequiredStatusChecks []struct {
							Context string `json:"context"`
						} `json:"required_status_checks"`
					} `json:"parameters"`
				} `json:"rules"`
			} `json:"payload"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode dry-run report: %v\n%s", err, output)
	}
	if report.Mode != "dry-run" || len(report.Operations) != 2 || report.Operations[0].Kind != "create_checks_ruleset" || report.Operations[1].Kind != "add_app_approval_bypass" {
		t.Fatalf("report = %+v, want checks before approval bypass", report)
	}
	checks := report.Operations[0].Payload
	if checks.Name != "software-factory-required-checks" || len(checks.BypassActors) != 0 || len(checks.Rules) != 1 || checks.Rules[0].Type != "required_status_checks" || len(checks.Rules[0].Parameters.RequiredStatusChecks) != 1 || checks.Rules[0].Parameters.RequiredStatusChecks[0].Context != "test-software-factory" {
		t.Fatalf("checks payload = %+v, want one non-bypassable test-software-factory check", checks)
	}
	approval := report.Operations[1].Payload
	if len(approval.BypassActors) != 2 || approval.BypassActors[0].ActorID != 4399553 || approval.BypassActors[0].ActorType != "Integration" || approval.BypassActors[0].BypassMode != "pull_request" || approval.BypassActors[1].ActorID != 6991398 || approval.BypassActors[1].ActorType != "User" || approval.BypassActors[1].BypassMode != "always" {
		t.Fatalf("approval bypass payload = %+v, want reviewed App and User actors", approval.BypassActors)
	}
}

func TestConfigureGitHubPolicyApplyCreatesChecksBeforeApprovalBypassAndIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newPolicyFixture(t, false)
	if output, err := fixture.run(t, "--apply"); err != nil {
		t.Fatalf("configure apply: %v\n%s", err, output)
	}
	if got := strings.Fields(fixture.mutations(t)); strings.Join(got, " ") != "POST-checks PUT-approval" {
		t.Fatalf("mutation order = %q, want POST-checks PUT-approval", fixture.mutations(t))
	}
	fixture.clearMutations(t)
	if output, err := fixture.run(t, "--apply"); err != nil {
		t.Fatalf("configure idempotent apply: %v\n%s", err, output)
	}
	if strings.TrimSpace(fixture.mutations(t)) != "" {
		t.Fatalf("idempotent apply mutations = %q, want none", fixture.mutations(t))
	}
}

func TestConfigureGitHubPolicyRefusesApprovalRulesetDriftBeforeMutation(t *testing.T) {
	t.Parallel()
	fixture := newPolicyFixture(t, true)
	output, err := fixture.run(t, "--apply")
	if err == nil || !strings.Contains(output, "approval ruleset precondition failed") {
		t.Fatalf("configure drift = %v\n%s, want precondition refusal", err, output)
	}
	if strings.TrimSpace(fixture.mutations(t)) != "" {
		t.Fatalf("drifted apply mutations = %q, want none", fixture.mutations(t))
	}
}

type policyFixture struct {
	dir    string
	script string
}

func newPolicyFixture(t *testing.T, drift bool) policyFixture {
	t.Helper()
	dir := t.TempDir()
	bypass := `[{
      "actor_id": 6991398,
      "actor_type": "User",
      "bypass_mode": "always"
    }]`
	if drift {
		bypass = `[{"actor_id":17,"actor_type":"Team","bypass_mode":"always"}]`
	}
	approval := `{
  "id": 20075698,
  "name": "main-require-codeowner-approval",
  "target": "branch",
  "enforcement": "active",
  "conditions": {"ref_name":{"include":["refs/heads/main"],"exclude":[]}},
  "bypass_actors": ` + bypass + `,
  "rules": [{"type":"pull_request","parameters":{"required_approving_review_count":1,"require_code_owner_review":true,"allowed_merge_methods":["merge","squash","rebase"]}}]
}`
	writeFixture(t, dir, "approval.json", approval)
	writeFixture(t, dir, "rulesets.json", `[{
  "id": 20075698,
  "name": "main-require-codeowner-approval",
  "target": "branch",
  "enforcement": "active"
}]`)
	fake := `#!/usr/bin/env bash
set -euo pipefail
args="$*"
input=""
want_input=0
for arg in "$@"; do
  if ((want_input)); then input="$arg"; want_input=0; continue; fi
  if [[ "$arg" == "--input" ]]; then want_input=1; fi
done
if [[ "$args" == *"--method POST"* ]]; then
  printf 'POST-checks\n' >>"$POLICY_FIXTURE_DIR/mutations"
  cp "$input" "$POLICY_FIXTURE_DIR/checks.json"
  printf '[{"id":30000001,"name":"software-factory-required-checks","target":"branch","enforcement":"active"}]\n' >"$POLICY_FIXTURE_DIR/rulesets.json"
  cat "$POLICY_FIXTURE_DIR/checks.json"
elif [[ "$args" == *"--method PUT"* ]]; then
  printf 'PUT-approval\n' >>"$POLICY_FIXTURE_DIR/mutations"
  jq '. + {id:20075698}' "$input" >"$POLICY_FIXTURE_DIR/approval.json"
  cat "$POLICY_FIXTURE_DIR/approval.json"
elif [[ "$args" == *"/rulesets/20075698"* ]]; then
  cat "$POLICY_FIXTURE_DIR/approval.json"
elif [[ "$args" == *"/rulesets/30000001"* ]]; then
  cat "$POLICY_FIXTURE_DIR/checks.json"
elif [[ "$args" == *"rulesets?includes_parents=false"* ]]; then
  cat "$POLICY_FIXTURE_DIR/rulesets.json"
else
  printf 'unexpected fake gh invocation: %s\n' "$args" >&2
  exit 90
fi
`
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(fake), 0o700); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return policyFixture{dir: dir, script: "configure-github-policy.sh"}
}

func (fixture policyFixture) run(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	args := []string{
		fixture.script,
		"--repository", "0x63616c/world-wide-webb",
		"--approval-ruleset-id", approvalRulesetID,
		"--app-id", factoryAppID,
		"--user-id", ownerUserID,
		"--branch", "main",
	}
	args = append(args, extra...)
	command := exec.Command("bash", args...)
	command.Dir = "."
	command.Env = append(os.Environ(), "GH_BIN="+filepath.Join(fixture.dir, "gh"), "POLICY_FIXTURE_DIR="+fixture.dir)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (fixture policyFixture) mutations(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(fixture.dir, "mutations"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read mutations: %v", err)
	}
	return string(content)
}

func (fixture policyFixture) clearMutations(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.dir, "mutations"), nil, 0o600); err != nil {
		t.Fatalf("clear mutations: %v", err)
	}
}

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
