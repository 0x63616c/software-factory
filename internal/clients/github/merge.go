package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/0x63616c/software-factory/internal/work"
	gh "github.com/google/go-github/v78/github"
)

const pullRequestMergeStateQuery = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      number
      id
      state
      headRefOid
      baseRefOid
      mergeable
      merged
      mergeCommit { oid }
    }
  }
}`

// mergeDiagnosticMaxBytes keeps untrusted GitHub text small in activity results
// and Temporal history while leaving enough context for the next Step.
const mergeDiagnosticMaxBytes = 2 << 10

type pullRequestMergeStateResponse struct {
	Data struct {
		Repository struct {
			PullRequest *graphQLPullRequest `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

type graphQLPullRequest struct {
	Number      int    `json:"number"`
	NodeID      string `json:"id"`
	State       string `json:"state"`
	HeadSHA     string `json:"headRefOid"`
	BaseSHA     string `json:"baseRefOid"`
	Mergeable   string `json:"mergeable"`
	Merged      bool   `json:"merged"`
	MergeCommit *struct {
		SHA string `json:"oid"`
	} `json:"mergeCommit"`
}

// MergePullRequest asks GitHub to squash-merge one reviewed pull-request head.
//
// A response is only confirmed when GitHub explicitly says merged and supplies
// the merge commit SHA. Every ambiguous outcome is reconciled from GraphQL so
// a lost REST response cannot cause a second semantic merge request.
func (c *Client) MergePullRequest(ctx context.Context, number int, expectedHeadSHA string) (work.PullRequestMergeResult, error) {
	op := fmt.Sprintf("squash-merging pull request #%d at %s", number, expectedHeadSHA)
	if number <= 0 || expectedHeadSHA == "" {
		return c.recordMergeError(ctx, number, expectedHeadSHA, permanent(op, ErrInvalid, errors.New("pull request number and expected head sha are required")))
	}

	merged, _, err := c.api.PullRequests.Merge(ctx, c.owner, c.repo, number, "", &gh.PullRequestOptions{
		MergeMethod: "squash",
		SHA:         expectedHeadSHA,
	})
	if err == nil && merged.GetMerged() && merged.GetSHA() != "" {
		return c.recordMergeOutcome(ctx, number, expectedHeadSHA, work.PullRequestMergeResult{
			Outcome:  work.PullRequestMergeConfirmed,
			MergeSHA: merged.GetSHA(),
		})
	}
	if alreadyClassified(err) {
		return c.recordMergeError(ctx, number, expectedHeadSHA, classify(ctx, op, err))
	}
	if mergePermissionRejected(err) {
		return c.recordMergeError(ctx, number, expectedHeadSHA, rulesetRejected(op, err))
	}
	if err != nil && !mergeResponseNeedsReconciliation(ctx, err) {
		return c.recordMergeError(ctx, number, expectedHeadSHA, classify(ctx, op, err))
	}

	// A merge response is an at-least-once boundary. GitHub can accept it while
	// the connection is lost, and its 405/409/422 answers overlap normal state
	// changes. Reconciliation is therefore mandatory before classification.
	state, reconcileErr := c.pullRequestMergeState(ctx, number)
	if reconcileErr != nil {
		if err == nil {
			return c.recordMergeError(ctx, number, expectedHeadSHA, reconcileErr)
		}
		return c.recordMergeError(ctx, number, expectedHeadSHA, fmt.Errorf("%s: reconciling the ambiguous merge response: %w", op, reconcileErr))
	}

	diagnostic := mergeMessage(err, merged)
	boundedDiagnostic := boundedMergeDiagnostic(diagnostic)
	result := classifyMergeState(state, expectedHeadSHA, diagnostic)
	if result.Outcome == work.PullRequestMergeRetryableAmbiguity && repositoryPolicyRejected(diagnostic) {
		return c.recordMergeError(ctx, number, expectedHeadSHA, rulesetRejected(op, errors.New(boundedDiagnostic)))
	}
	return c.recordMergeOutcome(ctx, number, expectedHeadSHA, result)
}

func (c *Client) recordMergeOutcome(
	ctx context.Context,
	number int,
	expectedHeadSHA string,
	result work.PullRequestMergeResult,
) (work.PullRequestMergeResult, error) {
	if result.Outcome == work.PullRequestMergeConfirmed {
		c.log.InfoContext(ctx, "classified pull request merge outcome",
			"pull_request", number,
			"expected_head_sha", expectedHeadSHA,
			"outcome", result.Outcome,
			"merge_sha", result.MergeSHA,
		)
		return result, nil
	}
	result.Diagnostic = boundedMergeDiagnostic(result.Diagnostic)

	c.log.InfoContext(ctx, "classified pull request merge outcome",
		"pull_request", number,
		"expected_head_sha", expectedHeadSHA,
		"outcome", result.Outcome,
	)
	return result, nil
}

func (c *Client) recordMergeError(
	ctx context.Context,
	number int,
	expectedHeadSHA string,
	err error,
) (work.PullRequestMergeResult, error) {
	c.log.ErrorContext(ctx, "pull request merge failed",
		"pull_request", number,
		"expected_head_sha", expectedHeadSHA,
		"outcome", "failed",
		"error", err,
	)
	return work.PullRequestMergeResult{}, err
}

func mergePermissionRejected(err error) bool {
	var response *gh.ErrorResponse
	return errors.As(err, &response) && response.Response.StatusCode == http.StatusForbidden
}

func rulesetRejected(op string, cause error) error {
	return fmt.Errorf("%s: %w: %w", op, ErrRuleset, cause)
}

// mergeResponseNeedsReconciliation identifies responses whose HTTP outcome
// cannot say whether GitHub performed the merge. Known transient and
// rate-limit responses retain classify's Temporal retry taxonomy instead.
func mergeResponseNeedsReconciliation(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || alreadyClassified(err) {
		return false
	}

	var response *gh.ErrorResponse
	if errors.As(err, &response) {
		switch response.Response.StatusCode {
		case http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity:
			return true
		default:
			return false
		}
	}

	// No HTTP response is a lost-response boundary: the request may have
	// reached GitHub after the connection stopped carrying its answer.
	return true
}

func (c *Client) pullRequestMergeState(ctx context.Context, number int) (state graphQLPullRequest, err error) {
	op := fmt.Sprintf("reading authoritative merge state for pull request #%d", number)
	body, err := json.Marshal(graphQLRequest{
		Query: pullRequestMergeStateQuery,
		Variables: map[string]any{
			"owner":  c.owner,
			"repo":   c.repo,
			"number": number,
		},
	})
	if err != nil {
		return graphQLPullRequest{}, fmt.Errorf("%s: encoding the graphql request: %w", op, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return graphQLPullRequest{}, fmt.Errorf("%s: building the graphql request: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client().Do(req)
	if err != nil {
		return graphQLPullRequest{}, fmt.Errorf("%s: %w", op, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("%s: closing the graphql response: %w", op, closeErr))
		}
	}()

	// CheckResponse consumes and replaces the body on a non-2xx response. Put
	// the original body back so the defer closes the network response whose
	// close error matters, rather than the in-memory replacement.
	responseBody := resp.Body
	checked := gh.CheckResponse(resp)
	resp.Body = responseBody
	if checked != nil {
		return graphQLPullRequest{}, classify(ctx, op, checked)
	}

	var decoded pullRequestMergeStateResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return graphQLPullRequest{}, fmt.Errorf("%s: decoding the graphql response: %w", op, err)
	}
	if len(decoded.Errors) > 0 {
		return graphQLPullRequest{}, classifyGraphQLErrors(op, decoded.Errors)
	}
	if decoded.Data.Repository.PullRequest == nil {
		return graphQLPullRequest{}, permanent(op, ErrNotFound, errors.New("github returned no pull request"))
	}
	return *decoded.Data.Repository.PullRequest, nil
}

func classifyMergeState(state graphQLPullRequest, expectedHeadSHA, diagnostic string) work.PullRequestMergeResult {
	pr := work.PullRequest{
		Number:       state.Number,
		NodeID:       state.NodeID,
		State:        graphQLPullRequestState(state.State),
		HeadSHA:      state.HeadSHA,
		BaseSHA:      state.BaseSHA,
		Mergeability: graphQLMergeability(state.Mergeable),
	}
	if state.MergeCommit != nil {
		pr.MergeSHA = state.MergeCommit.SHA
	}

	if state.Merged && pr.HeadSHA == "" {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeRetryableAmbiguity, PullRequest: pr, Diagnostic: "github reported merged without the pull request head sha"}
	}
	if state.Merged && pr.HeadSHA != expectedHeadSHA {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeHeadChanged, PullRequest: pr, Diagnostic: diagnostic}
	}
	if state.Merged && pr.MergeSHA != "" {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeConfirmed, MergeSHA: pr.MergeSHA, PullRequest: pr}
	}
	if state.Merged {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeRetryableAmbiguity, PullRequest: pr, Diagnostic: "github reported merged without a merge commit sha"}
	}
	if pr.State == work.PullRequestStateClosed {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeClosedUnmerged, PullRequest: pr, Diagnostic: diagnostic}
	}
	if pr.HeadSHA != expectedHeadSHA {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeHeadChanged, PullRequest: pr, Diagnostic: diagnostic}
	}
	if pr.Mergeability == work.PullRequestMergeabilityConflicting {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeTextConflict, PullRequest: pr, Diagnostic: diagnostic}
	}
	if baseRefreshRequired(diagnostic) {
		return work.PullRequestMergeResult{Outcome: work.PullRequestMergeBaseRefreshRequired, PullRequest: pr, Diagnostic: diagnostic}
	}
	return work.PullRequestMergeResult{Outcome: work.PullRequestMergeRetryableAmbiguity, PullRequest: pr, Diagnostic: diagnostic}
}

func graphQLPullRequestState(state string) work.PullRequestState {
	switch state {
	case "OPEN":
		return work.PullRequestStateOpen
	case "CLOSED":
		return work.PullRequestStateClosed
	default:
		return ""
	}
}

func graphQLMergeability(mergeable string) work.PullRequestMergeability {
	switch mergeable {
	case "MERGEABLE":
		return work.PullRequestMergeabilityMergeable
	case "CONFLICTING":
		return work.PullRequestMergeabilityConflicting
	default:
		return work.PullRequestMergeabilityUnknown
	}
}

func mergeMessage(err error, result *gh.PullRequestMergeResult) string {
	if result != nil && result.GetMessage() != "" {
		return result.GetMessage()
	}
	var response *gh.ErrorResponse
	if errors.As(err, &response) {
		return response.Message
	}
	return ""
}

func boundedMergeDiagnostic(diagnostic string) string {
	return truncateUTF8(diagnostic, mergeDiagnosticMaxBytes)
}

func baseRefreshRequired(diagnostic string) bool {
	diagnostic = strings.ToLower(diagnostic)
	return strings.Contains(diagnostic, "base branch was modified") ||
		strings.Contains(diagnostic, "base branch must be up to date") ||
		strings.Contains(diagnostic, "not up to date")
}

func repositoryPolicyRejected(diagnostic string) bool {
	diagnostic = strings.ToLower(diagnostic)
	return strings.Contains(diagnostic, "approving review is required") ||
		strings.Contains(diagnostic, "review required") ||
		strings.Contains(diagnostic, "protected branch") ||
		strings.Contains(diagnostic, "required status check") ||
		strings.Contains(diagnostic, "ruleset")
}
