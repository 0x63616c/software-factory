package github

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

const testBranch = "software-factory/ticket-328/0198f2c1-0000-7000-8000-000000000001"

func TestPullRequestForBranchReturnsWhatGitHubSaysIsOpenOnIt(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"number":           9,
			"html_url":         "https://github.com/" + testOwner + "/" + testRepo + "/pull/9",
			"url":              "https://api.github.com/repos/" + testOwner + "/" + testRepo + "/pulls/9",
			"node_id":          "PR_kwDOtest9",
			"title":            "fix: the thing",
			"body":             "does the thing",
			"draft":            true,
			"state":            "open",
			"head":             map[string]any{"sha": "head-sha"},
			"base":             map[string]any{"sha": "base-sha"},
			"mergeable_state":  "clean",
			"merge_commit_sha": "unconfirmed-merge-sha",
		}})
	})
	c, _ := s.client(t)

	pr, found, err := c.PullRequestForBranch(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("PullRequestForBranch: %v", err)
	}

	if !found || pr.Number != 9 {
		t.Fatalf("pr = %+v found = %v, want #9", pr, found)
	}
	// The page a human opens, not the API URL — the status comment links it.
	if pr.URL != "https://github.com/"+testOwner+"/"+testRepo+"/pull/9" {
		t.Fatalf("url = %q, want the html url", pr.URL)
	}
	// The GraphQL node id, not the REST number — convertPullRequestToDraft
	// accepts only this one (#435).
	if pr.NodeID != "PR_kwDOtest9" {
		t.Fatalf("node_id = %q, want PR_kwDOtest9", pr.NodeID)
	}
	if pr.Title != "fix: the thing" || pr.Body != "does the thing" {
		t.Fatalf("title/body = %q/%q, want the ones github reported", pr.Title, pr.Body)
	}
	if !pr.Draft {
		t.Fatal("draft = false, want the state github reported")
	}
	if pr.State != work.PullRequestStateOpen || pr.HeadSHA != "head-sha" || pr.BaseSHA != "base-sha" {
		t.Fatalf("authoritative pull request fields = %+v, want open head/base state", pr)
	}
	if pr.Mergeability != work.PullRequestMergeabilityMergeable || pr.MergeSHA != "unconfirmed-merge-sha" {
		t.Fatalf("merge fields = %+v, want GitHub's mergeability and SHA without inferring a merge", pr)
	}
}

func TestPullRequestForBranchRefusesAPullRequestWithNoNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"number":   9,
			"html_url": "https://github.com/" + testOwner + "/" + testRepo + "/pull/9",
		}})
	})
	c, _ := s.client(t)

	// Without a node id, ConvertPullRequestToDraft has nothing to convert —
	// this must fail loudly here rather than surface later as an opaque
	// GraphQL error with an empty id.
	if _, _, err := c.PullRequestForBranch(t.Context(), testBranch); err == nil {
		t.Fatal("a pull request with no node id would silently break draft conversion later")
	}
}

func TestPullRequestForBranchQualifiesTheHeadWithTheOwner(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	c, _ := s.client(t)

	if _, _, err := c.PullRequestForBranch(t.Context(), testBranch); err != nil {
		t.Fatalf("PullRequestForBranch: %v", err)
	}

	query := s.first(t, "GET /repos/"+testOwner+"/"+testRepo+"/pulls").Query
	// Unqualified, GitHub matches a branch of this name in every fork, and can
	// answer with someone else's pull request — which then reaches the ticket
	// as ours.
	if got, want := query.Get("head"), testOwner+":"+testBranch; got != want {
		t.Fatalf("head = %q, want %q", got, want)
	}
	if got := query.Get("state"); got != "open" {
		t.Fatalf("state = %q, want open — a closed pull request is not a proposal", got)
	}
}

func TestPullRequestForBranchReportsAbsenceWithoutFailing(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	c, _ := s.client(t)

	pr, found, err := c.PullRequestForBranch(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("a run that opened no pull request is blocked, not broken: %v", err)
	}
	if found || pr != (work.PullRequest{}) {
		t.Fatalf("pr = %+v found = %v, want nothing", pr, found)
	}
}

func TestPullRequestForBranchRefusesAPullRequestWithNoURL(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 9}})
	})
	c, _ := s.client(t)

	if _, _, err := c.PullRequestForBranch(t.Context(), testBranch); err == nil {
		t.Fatal("a pull request with no url would be reported to the ticket as an empty link")
	}
}
