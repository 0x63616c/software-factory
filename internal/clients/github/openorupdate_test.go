package github

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// TestOpenOrUpdatePullRequestCreatesOnTheDefaultBranchWhenNoneExists proves
// the "existing is nil" half of create-or-edit: it opens a new pull request
// against the repository's own default branch, and reports the node id and
// number github minted for later calls (draft conversion, comments).
func TestOpenOrUpdatePullRequestCreatesOnTheDefaultBranchWhenNoneExists(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
	})
	s.handle("POST /repos/"+testOwner+"/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/" + testOwner + "/" + testRepo + "/pull/42",
			"node_id":  "PR_new42",
			"title":    "fix: it",
			"body":     "the fix",
			"draft":    true,
		})
	})
	c, _ := s.client(t)

	pr, err := c.OpenOrUpdatePullRequest(t.Context(), testBranch, "fix: it", "the fix", nil)
	if err != nil {
		t.Fatalf("OpenOrUpdatePullRequest: %v", err)
	}
	if pr.Number != 42 || pr.NodeID != "PR_new42" {
		t.Fatalf("pr = %+v, want #42 with node id PR_new42", pr)
	}
	if !pr.Draft {
		t.Fatal("created pull request is not recorded as draft")
	}

	create := s.first(t, "POST /repos/"+testOwner+"/"+testRepo+"/pulls")
	body := decodeBody(t, create)
	if body["base"] != "main" {
		t.Fatalf("base = %v, want the repository's default branch", body["base"])
	}
	if body["head"] != testBranch {
		t.Fatalf("head = %v, want %q", body["head"], testBranch)
	}
	if body["draft"] != true {
		t.Fatalf("draft = %v, want true", body["draft"])
	}
}

// TestOpenOrUpdatePullRequestEditsWhenTitleOrBodyChanged proves the "existing,
// and different" half: it calls Edit rather than Create, and keeps the
// existing number, url and node id — Edit's response never carries those the
// way Create's does.
func TestOpenOrUpdatePullRequestEditsWhenTitleOrBodyChanged(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PATCH /repos/"+testOwner+"/"+testRepo+"/pulls/9", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "fix: it v2", "body": "the fix, updated", "draft": true})
	})
	c, _ := s.client(t)

	existing := &work.PullRequest{Number: 9, URL: "https://github.com/o/r/pull/9", Draft: true, NodeID: "PR_9", Title: "fix: it", Body: "the fix"}
	pr, err := c.OpenOrUpdatePullRequest(t.Context(), testBranch, "fix: it v2", "the fix, updated", existing)
	if err != nil {
		t.Fatalf("OpenOrUpdatePullRequest: %v", err)
	}
	if pr.Number != 9 || pr.NodeID != "PR_9" || pr.URL != existing.URL {
		t.Fatalf("pr = %+v, want #9's identity preserved", pr)
	}
	if pr.Title != "fix: it v2" || pr.Body != "the fix, updated" {
		t.Fatalf("pr title/body = %q/%q, want the edited ones", pr.Title, pr.Body)
	}
	if !pr.Draft {
		t.Fatal("edited pull request lost its reported draft state")
	}
	if s.count("POST /repos/"+testOwner+"/"+testRepo+"/pulls") != 0 {
		t.Fatal("an existing pull request was created a second time instead of edited")
	}
}

// TestOpenOrUpdatePullRequestIsANoOpWhenNothingChanged proves the idempotent
// half: a push whose title and body implement/review already told GitHub
// about must not spend an Edit call it does not need.
func TestOpenOrUpdatePullRequestIsANoOpWhenNothingChanged(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PATCH /repos/"+testOwner+"/"+testRepo+"/pulls/9", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("Edit was called for a pull request whose title and body did not change")
	})
	c, _ := s.client(t)

	existing := &work.PullRequest{Number: 9, URL: "https://github.com/o/r/pull/9", NodeID: "PR_9", Title: "fix: it", Body: "the fix"}
	pr, err := c.OpenOrUpdatePullRequest(t.Context(), testBranch, "fix: it", "the fix", existing)
	if err != nil {
		t.Fatalf("OpenOrUpdatePullRequest: %v", err)
	}
	if pr != *existing {
		t.Fatalf("pr = %+v, want the existing value unchanged", pr)
	}
}
