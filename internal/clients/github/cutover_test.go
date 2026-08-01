package github

import (
	"net/http"
	"testing"
)

func TestLegacyPullRequestsReturnsOnlyOpenPreRedesignBranchesWithAutoMergeState(t *testing.T) {
	t.Parallel()
	const legacyBranch = "software-factory/ticket-7/56b6ef17-4ce3-4ae6-a66f-2aa7f0c1da70"
	s, _ := newStub(t)
	s.handle("GET /repos/0x63616c/world-wide-webb/pulls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"number": 41, "head": map[string]any{"ref": legacyBranch}},
			{"number": 42, "head": map[string]any{"ref": "software-factory/factory-ticket-7/target-run"}},
			{"number": 43, "head": map[string]any{"ref": "human/branch"}},
		})
	})
	s.handle("GET /repos/0x63616c/world-wide-webb/pulls/41", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"number": 41, "node_id": "PR_41", "html_url": "https://github.example/pull/41",
			"head":       map[string]any{"ref": legacyBranch},
			"auto_merge": map[string]any{"merge_method": "squash"},
		})
	})
	client, _ := s.client(t)

	got, err := client.LegacyPullRequests(t.Context())
	if err != nil {
		t.Fatalf("LegacyPullRequests: %v", err)
	}
	if len(got) != 1 || got[0].Number != 41 || got[0].NodeID != "PR_41" || !got[0].AutoMergeEnabled {
		t.Fatalf("LegacyPullRequests() = %+v, want only auto-merge-enabled legacy PR 41", got)
	}
	if s.count("GET /repos/0x63616c/world-wide-webb/pulls/42") != 0 {
		t.Fatal("target PR was fetched as a legacy PR")
	}
}

func TestLegacyFactoryBranchRejectsLookalikes(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{
		"software-factory/ticket-0/run",
		"software-factory/ticket-01/run",
		"software-factory/ticket--1/run",
		"software-factory/ticket-1/not-a-temporal-run-id",
		"software-factory/factory-ticket-1/run",
		"software-factory/ticket-1/run/extra",
	} {
		if legacyFactoryBranch(branch) {
			t.Errorf("legacyFactoryBranch(%q) = true", branch)
		}
	}
}
