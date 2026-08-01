package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/0x63616c/software-factory/internal/work"
	gh "github.com/google/go-github/v78/github"
)

const mergePath = "/repos/" + testOwner + "/" + testRepo + "/pulls/9/merge"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type closeErrorBody struct {
	io.ReadCloser
	err error
}

func (b closeErrorBody) Close() error {
	return errors.Join(b.ReadCloser.Close(), b.err)
}

func TestMergePullRequestSquashesTheExpectedHeadAndConfirmsTheReturnedSHA(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"merged": true, "sha": "merge-sha"})
	})
	c, logs := s.client(t)

	result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if result.Outcome != work.PullRequestMergeConfirmed || result.MergeSHA != "merge-sha" {
		t.Fatalf("result = %+v, want a confirmed merge-sha", result)
	}
	assertMergeOutcomeLog(t, logs, work.PullRequestMergeConfirmed, "merge-sha")

	sent := decodeBody(t, s.first(t, "PUT "+mergePath))
	if sent["merge_method"] != "squash" || sent["sha"] != "reviewed-head" || len(sent) != 2 {
		t.Fatalf("merge body = %v, want only squash and the reviewed head", sent)
	}
	if got := s.count("POST /graphql"); got != 0 {
		t.Fatalf("made %d GraphQL calls after a confirmed REST merge, want no target-path review or auto-merge mutation", got)
	}
}

func TestMergePullRequestConfirmsAMergeOnlyFromGraphQLMergedAndMergeCommit(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusConflict, "merge response lost")
	})
	s.handle("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var request graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decoding reconciliation query: %v", err)
		}
		if !strings.Contains(request.Query, "mergeCommit") {
			t.Fatal("reconciliation query does not request mergeCommit")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"number": 9, "id": "PR_kwDOtest9", "state": "CLOSED", "headRefOid": "reviewed-head", "baseRefOid": "base-sha", "mergeable": "MERGEABLE", "merged": true,
			"mergeCommit": map[string]any{"oid": "merge-sha"},
		}}}})
	})
	c, logs := s.client(t)

	result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if result.Outcome != work.PullRequestMergeConfirmed || result.MergeSHA != "merge-sha" {
		t.Fatalf("result = %+v, want authoritative confirmed merge", result)
	}
	assertMergeOutcomeLog(t, logs, work.PullRequestMergeConfirmed, "merge-sha")
}

func TestMergePullRequestDoesNotConfirmADifferentHeadMergedAfterALostResponse(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusConflict, "merge response lost")
	})
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": reconciledPullRequest("CLOSED", "replacement-head", "MERGEABLE", true, "replacement-merge")}}})
	})
	c, _ := s.client(t)

	result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if result.Outcome != work.PullRequestMergeHeadChanged || result.MergeSHA != "" {
		t.Fatalf("result = %+v, want head-changed without confirming the replacement merge", result)
	}
}

func TestMergePullRequestDoesNotTreatClosedOrMergeSHAAloneAsAConfirmedMerge(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusUnprocessableEntity, "merge could not be completed")
	})
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"number": 9, "id": "PR_kwDOtest9", "state": "CLOSED", "headRefOid": "reviewed-head", "baseRefOid": "base-sha", "mergeable": "MERGEABLE", "merged": false,
			"mergeCommit": map[string]any{"oid": "unconfirmed-sha"},
		}}}})
	})
	c, _ := s.client(t)

	result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if result.Outcome != work.PullRequestMergeClosedUnmerged {
		t.Fatalf("result = %+v, want closed-unmerged", result)
	}
}

func TestMergePullRequestDoesNotConfirmA200ResponseWithMergedFalse(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"merged": false, "sha": "", "message": "Pull Request is not mergeable"})
	})
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": reconciledPullRequest("OPEN", "reviewed-head", "UNKNOWN", false, "")}}})
	})
	c, logs := s.client(t)

	result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if result.Outcome != work.PullRequestMergeRetryableAmbiguity || result.Diagnostic != "Pull Request is not mergeable" {
		t.Fatalf("result = %+v, want retryable ambiguity with GitHub's diagnostic", result)
	}
	assertMergeOutcomeLog(t, logs, work.PullRequestMergeRetryableAmbiguity, "")
}

func TestMergePullRequestBoundsExternalDiagnosticsWithoutSplittingUTF8(t *testing.T) {
	t.Parallel()

	const wantDiagnosticMaxBytes = 2 << 10
	oversized := strings.Repeat("界", wantDiagnosticMaxBytes)
	cases := map[string]struct {
		status       int
		mergeability string
		want         work.PullRequestMergeOutcome
	}{
		"HTTP 200 merged false": {
			status:       http.StatusOK,
			mergeability: "UNKNOWN",
			want:         work.PullRequestMergeRetryableAmbiguity,
		},
		"conflict response": {
			status:       http.StatusConflict,
			mergeability: "CONFLICTING",
			want:         work.PullRequestMergeTextConflict,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, _ := newStub(t)
			s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
				if tc.status == http.StatusOK {
					writeJSON(w, tc.status, map[string]any{"merged": false, "sha": "", "message": oversized})
					return
				}
				writeError(w, tc.status, oversized)
			})
			s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{
					"pullRequest": reconciledPullRequest("OPEN", "reviewed-head", tc.mergeability, false, ""),
				}}})
			})
			c, logs := s.client(t)

			result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
			if err != nil {
				t.Fatalf("MergePullRequest: %v", err)
			}
			if result.Outcome != tc.want {
				t.Fatalf("outcome = %s, want %s", result.Outcome, tc.want)
			}
			if len(result.Diagnostic) == 0 || len(result.Diagnostic) > wantDiagnosticMaxBytes {
				t.Fatalf("diagnostic bytes = %d, want 1..%d", len(result.Diagnostic), wantDiagnosticMaxBytes)
			}
			if !utf8.ValidString(result.Diagnostic) {
				t.Fatalf("diagnostic ends inside a UTF-8 rune: %q", result.Diagnostic)
			}
			assertMergeOutcomeLog(t, logs, tc.want, "")
		})
	}
}

func TestMergePullRequestDoesNotConfirmA200ResponseMissingTheMergeSHA(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"merged": true, "message": "Pull Request successfully merged"})
	})
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": reconciledPullRequest("CLOSED", "reviewed-head", "MERGEABLE", true, "")}}})
	})
	c, _ := s.client(t)

	result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if result.Outcome != work.PullRequestMergeRetryableAmbiguity || result.MergeSHA != "" {
		t.Fatalf("result = %+v, want retryable ambiguity without a merge SHA", result)
	}
}

func TestMergePullRequestClassifiesAForbiddenMergeAsRepairableRepositoryPolicy(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, "Resource not accessible by integration")
	})
	c, logs := s.client(t)

	_, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err == nil {
		t.Fatal("MergePullRequest succeeded despite a permission rejection")
	}
	if !errors.Is(err, ErrRuleset) || errors.Is(err, work.ErrPermanent) || errors.Is(err, ErrAuth) {
		t.Fatalf("error = %v, want a retryable repository-policy classification distinct from bad credentials", err)
	}
	assertMergeFailureLog(t, logs)
}

func TestMergePullRequestPreservesPreclassifiedForbiddenTransportErrors(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		classified error
		wantKind   error
		permanent  bool
	}{
		"authentication": {
			classified: fmt.Errorf("refreshing the installation token: %w (%w)", ErrAuth, work.ErrPermanent),
			wantKind:   ErrAuth,
			permanent:  true,
		},
		"rate limit": {
			classified: fmt.Errorf("refreshing the installation token: %w", ErrRateLimit),
			wantKind:   ErrRateLimit,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			forbidden := &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusForbidden},
				Message:  "token refresh forbidden",
			}
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("installation transport: %w: %w", tc.classified, forbidden)
			})
			s, _ := newStub(t)
			c, _ := s.clientWithOptions(t, WithHTTPClient(&http.Client{Transport: transport}))

			_, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
			if !errors.Is(err, tc.wantKind) || errors.Is(err, ErrRuleset) {
				t.Fatalf("error = %v, want preserved %v without repository-policy classification", err, tc.wantKind)
			}
			if got := errors.Is(err, work.ErrPermanent); got != tc.permanent {
				t.Fatalf("error permanence = %t, want %t: %v", got, tc.permanent, err)
			}
		})
	}
}

func TestMergePullRequestClassifiesPolicyRefusalsAfterAuthoritativeReconciliation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status  int
		message string
	}{
		"405 review required": {
			status:  http.StatusMethodNotAllowed,
			message: "At least 1 approving review is required by reviewers with write access.",
		},
		"409 protected branch": {
			status:  http.StatusConflict,
			message: "Protected branch update failed for refs/heads/main.",
		},
		"422 required check": {
			status:  http.StatusUnprocessableEntity,
			message: `Required status check "test-software-factory" is expected.`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newStub(t)
			s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, tc.status, tc.message)
			})
			s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": reconciledPullRequest("OPEN", "reviewed-head", "MERGEABLE", false, "")}}})
			})
			c, _ := s.client(t)

			_, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
			if err == nil {
				t.Fatal("MergePullRequest succeeded despite repository policy")
			}
			if !errors.Is(err, ErrRuleset) || errors.Is(err, work.ErrPermanent) {
				t.Fatalf("error = %v, want retryable repository-policy classification", err)
			}
		})
	}
}

func TestMergePullRequestKeepsBadCredentialsDistinctFromRepositoryPolicy(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusUnauthorized, "Bad credentials")
	})
	c, _ := s.client(t)

	_, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if !errors.Is(err, ErrAuth) || !errors.Is(err, work.ErrPermanent) || errors.Is(err, ErrRuleset) {
		t.Fatalf("error = %v, want permanent bad-credential classification", err)
	}
}

func TestMergePullRequestReconcilesALostResponseBeforeRetrying(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server response cannot simulate a dropped connection")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("hijacking merge response: %v", err)
		}
		_ = conn.Close()
	})
	s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": reconciledPullRequest("OPEN", "reviewed-head", "UNKNOWN", false, "")}}})
	})
	c, _ := s.client(t)

	result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if result.Outcome != work.PullRequestMergeRetryableAmbiguity {
		t.Fatalf("result = %+v, want retryable ambiguity after a dropped response", result)
	}
}

func TestMergePullRequestPreservesARateLimitsTemporalRetryClassification(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, "Too Many Requests")
	})
	c, _ := s.client(t)

	_, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
	if err == nil {
		t.Fatal("MergePullRequest succeeded despite a rate limit")
	}
	if !errors.Is(err, ErrRateLimit) || errors.Is(err, work.ErrPermanent) {
		t.Fatalf("error = %v, want a retryable rate-limit classification", err)
	}
	if got := s.count("POST /graphql"); got != 0 {
		t.Fatalf("made %d reconciliation calls for a known rate limit, want 0", got)
	}
}

func TestMergePullRequestReportsGraphQLBodyCloseErrorsWithoutDiscardingDecodeErrors(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status        int
		response      string
		wantPriorText string
	}{
		"after a valid response": {
			response: `{"data":{"repository":{"pullRequest":{"number":9,"id":"PR_kwDOtest9","state":"OPEN","headRefOid":"reviewed-head","baseRefOid":"base-sha","mergeable":"UNKNOWN","merged":false,"mergeCommit":null}}}}`,
		},
		"alongside a decode error": {
			response:      `{"data":`,
			wantPriorText: "decoding the graphql response",
		},
		"alongside a classified response error": {
			status:        http.StatusBadGateway,
			response:      `{"message":"upstream unavailable"}`,
			wantPriorText: "502 upstream unavailable",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, _ := newStub(t)
			s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusConflict, "merge response lost")
			})
			s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.response)
			})

			closeErr := errors.New("closing injected response body")
			base := http.DefaultTransport
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := base.RoundTrip(request)
				if err == nil && request.URL.Path == "/graphql" {
					response.Body = closeErrorBody{ReadCloser: response.Body, err: closeErr}
				}
				return response, err
			})
			c, _ := s.clientWithOptions(t, WithHTTPClient(&http.Client{Transport: transport}))

			_, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
			if !errors.Is(err, closeErr) {
				t.Fatalf("error = %v, want the response-body close error", err)
			}
			if !strings.Contains(err.Error(), "closing the graphql response") {
				t.Fatalf("error = %v, want close operation context", err)
			}
			if tc.wantPriorText != "" && !strings.Contains(err.Error(), tc.wantPriorText) {
				t.Fatalf("error = %v, want preserved prior error containing %q", err, tc.wantPriorText)
			}
		})
	}
}

func TestMergePullRequestClassifiesAnUnmergedReconciliation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status  int
		message string
		pr      map[string]any
		want    work.PullRequestMergeOutcome
	}{
		"text conflict": {
			status: http.StatusMethodNotAllowed,
			pr:     reconciledPullRequest("OPEN", "reviewed-head", "CONFLICTING", false, ""),
			want:   work.PullRequestMergeTextConflict,
		},
		"head changed": {
			status: http.StatusConflict,
			pr:     reconciledPullRequest("OPEN", "new-head", "MERGEABLE", false, ""),
			want:   work.PullRequestMergeHeadChanged,
		},
		"base refresh required": {
			status:  http.StatusUnprocessableEntity,
			message: "Base branch was modified. Review and try the merge again.",
			pr:      reconciledPullRequest("OPEN", "reviewed-head", "MERGEABLE", false, ""),
			want:    work.PullRequestMergeBaseRefreshRequired,
		},
		"mergeability computing": {
			status: http.StatusUnprocessableEntity,
			pr:     reconciledPullRequest("OPEN", "reviewed-head", "UNKNOWN", false, ""),
			want:   work.PullRequestMergeRetryableAmbiguity,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newStub(t)
			s.handle("PUT "+mergePath, func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, tc.status, tc.message)
			})
			s.handle("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": tc.pr}}})
			})
			c, _ := s.client(t)

			result, err := c.MergePullRequest(t.Context(), 9, "reviewed-head")
			if err != nil {
				t.Fatalf("MergePullRequest: %v", err)
			}
			if result.Outcome != tc.want {
				t.Fatalf("result = %+v, want %s", result, tc.want)
			}
		})
	}
}

func reconciledPullRequest(state, head, mergeable string, merged bool, mergeSHA string) map[string]any {
	pr := map[string]any{
		"number": 9, "id": "PR_kwDOtest9", "state": state, "headRefOid": head, "baseRefOid": "base-sha", "mergeable": mergeable, "merged": merged,
	}
	if mergeSHA == "" {
		pr["mergeCommit"] = nil
	} else {
		pr["mergeCommit"] = map[string]any{"oid": mergeSHA}
	}
	return pr
}

func assertMergeOutcomeLog(t *testing.T, logs *bytes.Buffer, outcome work.PullRequestMergeOutcome, mergeSHA string) {
	t.Helper()

	matches := matchingLogRecords(t, logs, "classified pull request merge outcome")
	if len(matches) != 1 {
		t.Fatalf("merge outcome logs = %v, want exactly one; all logs: %s", matches, logs.String())
	}

	record := matches[0]
	if record["pull_request"] != float64(9) || record["expected_head_sha"] != "reviewed-head" || record["outcome"] != string(outcome) {
		t.Fatalf("merge outcome log = %v, want pull request 9 at reviewed-head with outcome %s", record, outcome)
	}
	gotMergeSHA, hasMergeSHA := record["merge_sha"]
	if mergeSHA == "" && hasMergeSHA {
		t.Fatalf("merge outcome log = %v, want no merge_sha for a non-confirmed outcome", record)
	}
	if mergeSHA != "" && (!hasMergeSHA || gotMergeSHA != mergeSHA) {
		t.Fatalf("merge outcome log = %v, want merge_sha %q", record, mergeSHA)
	}
	if _, exists := record["diagnostic"]; exists {
		t.Fatalf("merge outcome log = %v, want no untrusted diagnostic", record)
	}
}

func assertMergeFailureLog(t *testing.T, logs *bytes.Buffer) {
	t.Helper()

	matches := matchingLogRecords(t, logs, "pull request merge failed")
	if len(matches) != 1 {
		t.Fatalf("merge failure logs = %v, want exactly one; all logs: %s", matches, logs.String())
	}
	record := matches[0]
	if record["pull_request"] != float64(9) || record["expected_head_sha"] != "reviewed-head" || record["outcome"] != "failed" {
		t.Fatalf("merge failure log = %v, want pull request 9 at reviewed-head with failed outcome", record)
	}
	if _, exists := record["merge_sha"]; exists {
		t.Fatalf("merge failure log = %v, want no merge_sha", record)
	}
}

func matchingLogRecords(t *testing.T, logs *bytes.Buffer, message string) []map[string]any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	var matches []map[string]any
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			return matches
		}
		if err != nil {
			t.Fatalf("decoding log record: %v", err)
		}
		if record["msg"] == message {
			matches = append(matches, record)
		}
	}
}
