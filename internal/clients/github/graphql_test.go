package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// TestConvertPullRequestToDraftPostsTheMutationWithTheNodeID proves the one
// fact this whole file exists for: the request carries the pull request's
// GraphQL node id, not its REST number — a REST number silently does not
// satisfy a GraphQL ID argument, and that would be a runtime error rather
// than a build error if this ever regressed to passing the wrong one.
func TestConvertPullRequestToDraftPostsTheMutationWithTheNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the graphql request: %v", err)
		}
		vars, _ := body["variables"].(map[string]any)
		if vars["id"] != "PR_kwDOtest9" {
			t.Errorf("variables.id = %v, want the pull request's node id", vars["id"])
		}
		query, _ := body["query"].(string)
		if !containsSubstr(query, "convertPullRequestToDraft") {
			t.Errorf("query = %q, want it to call convertPullRequestToDraft", query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"convertPullRequestToDraft": map[string]any{"pullRequest": map[string]any{"isDraft": true}}},
		})
	})
	c, _ := s.client(t)

	if err := c.ConvertPullRequestToDraft(t.Context(), "PR_kwDOtest9"); err != nil {
		t.Fatalf("ConvertPullRequestToDraft: %v", err)
	}
}

func TestMarkPullRequestReadyForReviewPostsTheMutationWithTheNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the graphql request: %v", err)
		}
		vars, _ := body["variables"].(map[string]any)
		if vars["id"] != "PR_kwDOtest9" {
			t.Errorf("variables.id = %v, want the pull request's node id", vars["id"])
		}
		query, _ := body["query"].(string)
		if !containsSubstr(query, "markPullRequestReadyForReview") {
			t.Errorf("query = %q, want it to call markPullRequestReadyForReview", query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"markPullRequestReadyForReview": map[string]any{"pullRequest": map[string]any{"isDraft": false}}},
		})
	})
	c, _ := s.client(t)

	if err := c.MarkPullRequestReadyForReview(t.Context(), "PR_kwDOtest9"); err != nil {
		t.Fatalf("MarkPullRequestReadyForReview: %v", err)
	}
}

func TestMarkPullRequestReadyForReviewRefusesAnEmptyNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("a mutation was sent for an empty node id")
	})
	c, _ := s.client(t)

	if err := c.MarkPullRequestReadyForReview(t.Context(), ""); err == nil {
		t.Fatal("want an error for an empty node id")
	}
}

func TestMarkPullRequestReadyForReviewClassifiesAnHTTPFailure(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusUnauthorized, "Bad credentials")
	})
	c, _ := s.client(t)

	err := c.MarkPullRequestReadyForReview(t.Context(), "PR_1")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestMarkPullRequestReadyForReviewClassifiesAGraphQLBodyError(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a PullRequest"}},
		})
	})
	c, _ := s.client(t)

	err := c.MarkPullRequestReadyForReview(t.Context(), "PR_gone")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEnablePullRequestAutoMergePostsTheMutationWithTheNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the graphql request: %v", err)
		}
		vars, _ := body["variables"].(map[string]any)
		if vars["id"] != "PR_kwDOtest9" {
			t.Errorf("variables.id = %v, want the pull request's node id", vars["id"])
		}
		query, _ := body["query"].(string)
		if !containsSubstr(query, "enablePullRequestAutoMerge") {
			t.Errorf("query = %q, want it to call enablePullRequestAutoMerge", query)
		}
		if !containsSubstr(query, "SQUASH") {
			t.Errorf("query = %q, want it to request a squash merge", query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"enablePullRequestAutoMerge": map[string]any{"pullRequest": map[string]any{"autoMergeRequest": map[string]any{"enabledAt": "2026-07-30T00:00:00Z"}}}},
		})
	})
	c, _ := s.client(t)

	if err := c.EnablePullRequestAutoMerge(t.Context(), "PR_kwDOtest9"); err != nil {
		t.Fatalf("EnablePullRequestAutoMerge: %v", err)
	}
}

func TestEnablePullRequestAutoMergeRefusesAnEmptyNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("a mutation was sent for an empty node id")
	})
	c, _ := s.client(t)

	if err := c.EnablePullRequestAutoMerge(t.Context(), ""); err == nil {
		t.Fatal("want an error for an empty node id")
	}
}

func TestEnablePullRequestAutoMergeClassifiesAnHTTPFailure(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusUnauthorized, "Bad credentials")
	})
	c, _ := s.client(t)

	err := c.EnablePullRequestAutoMerge(t.Context(), "PR_1")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestEnablePullRequestAutoMergeClassifiesAGraphQLBodyError(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a PullRequest"}},
		})
	})
	c, _ := s.client(t)

	err := c.EnablePullRequestAutoMerge(t.Context(), "PR_gone")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDisablePullRequestAutoMergePostsTheMutationWithTheNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the graphql request: %v", err)
		}
		vars, _ := body["variables"].(map[string]any)
		if vars["id"] != "PR_kwDOtest9" {
			t.Errorf("variables.id = %v, want the pull request's node id", vars["id"])
		}
		query, _ := body["query"].(string)
		if !containsSubstr(query, "disablePullRequestAutoMerge") {
			t.Errorf("query = %q, want it to call disablePullRequestAutoMerge", query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"disablePullRequestAutoMerge": map[string]any{"pullRequest": map[string]any{"autoMergeRequest": nil}}},
		})
	})
	c, _ := s.client(t)

	if err := c.DisablePullRequestAutoMerge(t.Context(), "PR_kwDOtest9"); err != nil {
		t.Fatalf("DisablePullRequestAutoMerge: %v", err)
	}
}

// TestConvertPullRequestToDraftRefusesAnEmptyNodeID proves this fails before
// ever posting a mutation with nothing to convert, rather than sending
// GitHub a request with a null id and getting back an opaque GraphQL error.
func TestConvertPullRequestToDraftRefusesAnEmptyNodeID(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("a mutation was sent for an empty node id")
	})
	c, _ := s.client(t)

	if err := c.ConvertPullRequestToDraft(t.Context(), ""); err == nil {
		t.Fatal("want an error for an empty node id")
	}
}

// TestConvertPullRequestToDraftClassifiesAnHTTPFailure proves an ordinary
// transport-level failure (a 401, here) is classified exactly as a REST call
// on this client would be — the same installationTransport authenticates
// both planes, and the same vocabulary describes both failing.
func TestConvertPullRequestToDraftClassifiesAnHTTPFailure(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusUnauthorized, "Bad credentials")
	})
	c, _ := s.client(t)

	err := c.ConvertPullRequestToDraft(t.Context(), "PR_1")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// TestConvertPullRequestToDraftClassifiesAGraphQLBodyError proves the other
// half GitHub's GraphQL API can fail through: a 200 OK response whose body
// carries an "errors" array, which CheckResponse's status-code check alone
// would miss entirely.
func TestConvertPullRequestToDraftClassifiesAGraphQLBodyError(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a PullRequest"}},
		})
	})
	c, _ := s.client(t)

	err := c.ConvertPullRequestToDraft(t.Context(), "PR_gone")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func containsSubstr(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
