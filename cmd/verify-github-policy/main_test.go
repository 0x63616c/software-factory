package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunWritesReadyReportForSplitRulesets(t *testing.T) {
	t.Parallel()
	input := `[
  {"name":"approval","enforcement":"active","target":"branch","conditions":{"ref_name":{"include":["refs/heads/main"],"exclude":[]}},"bypass_actors":[{"actor_id":42,"actor_type":"Integration","bypass_mode":"pull_request"}],"rules":[{"type":"pull_request","parameters":{"required_approving_review_count":1}}]},
  {"name":"checks","enforcement":"active","target":"branch","conditions":{"ref_name":{"include":["refs/heads/main"],"exclude":[]}},"bypass_actors":[],"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"test-software-factory"}]}}]}
]`
	var output bytes.Buffer
	if err := run([]string{"--app-id", "42", "--branch", "main"}, strings.NewReader(input), &output); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := output.String(), `"ready":true`; !strings.Contains(got, want) {
		t.Fatalf("report = %s, want %s", got, want)
	}
}

func TestRunWritesReportBeforeRejectingUnreadyPolicy(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	err := run([]string{"--app-id", "42"}, strings.NewReader(`[]`), &output)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("run error = %v, want not-ready error", err)
	}
	if got, want := output.String(), `"ready":false`; !strings.Contains(got, want) {
		t.Fatalf("report = %s, want %s", got, want)
	}
}
