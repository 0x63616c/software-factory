package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	gh "github.com/google/go-github/v78/github"
)

// This file is this package's only user of GitHub's GraphQL API, kept to
// exactly the mutations REST cannot express: changing an already-open pull
// request's draft state, and enabling merge-when-ready. See github.go's
// "Verified facts" above PullRequestForBranch for why REST has no path for
// draft state at all, and why go-github's PullRequestsService.Edit silently
// no-ops rather than erroring if asked to. Auto-merge is the same story:
// go-github v78's PullRequestsService has no auto-merge-enable method because
// REST has none — PullRequestsService.Merge merges immediately, it does not
// arm a PR to merge itself once its requirements are met; only the
// enablePullRequestAutoMerge mutation does that.
//
// No GraphQL client library is added for these few mutations. The request and
// response shapes here are the whole of what this service needs from GitHub's
// GraphQL API; a hand-rolled POST is narrower than a generated client whose
// types would cross this package's boundary the moment a caller touched them
// (AGENTS.md tenet 4 — no leaky abstractions), and it costs nothing this
// package's other methods do not already pay: authentication is the same
// installationTransport every REST call here uses.

// convertDraftMutation is GitHub's own mutation for un-readying a pull
// request.
const convertDraftMutation = `mutation($id: ID!) {
  convertPullRequestToDraft(input: {pullRequestId: $id}) {
    pullRequest {
      isDraft
    }
  }
}`

const markReadyMutation = `mutation($id: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $id}) {
    pullRequest {
      isDraft
    }
  }
}`

// enableAutoMergeMutation arms a pull request to merge itself, squash, the
// moment its branch protection requirements (the required approval) are met
// and its checks are green. It does not merge anything itself — that is what
// distinguishes it from PullRequestsService.Merge.
const enableAutoMergeMutation = `mutation($id: ID!) {
  enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: SQUASH}) {
    pullRequest {
      autoMergeRequest {
        enabledAt
      }
    }
  }
}`

const disableAutoMergeMutation = `mutation($id: ID!) {
  disablePullRequestAutoMerge(input: {pullRequestId: $id}) {
    pullRequest {
      autoMergeRequest {
        enabledAt
      }
    }
  }
}`

// graphQLRequest is the body every GraphQL call this package makes sends.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// graphQLError is one entry of a GraphQL response's "errors" array. GitHub's
// own errors carry a "type" alongside the message; see classifyGraphQLErrors
// for what each maps to.
type graphQLError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// graphQLResponse is the shape of every GraphQL response this package reads.
// Data is discarded — convertPullRequestToDraft's own answer (isDraft) is not
// consumed by anything, because the call site already knows what it asked
// for; only failure is worth decoding.
type graphQLResponse struct {
	Errors []graphQLError `json:"errors"`
}

// ConvertPullRequestToDraft marks an open pull request as a draft.
//
// It takes the pull request's GraphQL node id, not its REST number — the two
// are different identifiers and this mutation accepts only the former. See
// PullRequestForBranch and the create-or-edit path in github.go for where
// NodeID is populated on work.PullRequest.
func (c *Client) ConvertPullRequestToDraft(ctx context.Context, nodeID string) error {
	return c.runPullRequestMutation(ctx, nodeID, "converting", "to draft", convertDraftMutation, "converted pull request to draft")
}

// MarkPullRequestReadyForReview marks a draft pull request ready for human review.
func (c *Client) MarkPullRequestReadyForReview(ctx context.Context, nodeID string) error {
	return c.runPullRequestMutation(ctx, nodeID, "marking", "ready for review", markReadyMutation, "marked pull request ready for review")
}

// EnablePullRequestAutoMerge arms a pull request to squash-merge itself once
// its required approval and checks are satisfied. Call sites must only reach
// this once the pull request is already out of draft — GitHub accepts the
// mutation on a draft PR too, which would arm a still-iterating draft to
// merge the moment someone later approves it.
func (c *Client) EnablePullRequestAutoMerge(ctx context.Context, nodeID string) error {
	return c.runPullRequestMutation(ctx, nodeID, "enabling", "auto-merge", enableAutoMergeMutation, "enabled auto-merge on pull request")
}

// DisablePullRequestAutoMerge disarms a legacy PR before workflow ownership
// moves. It is safe to retry; GitHub leaves an already-disarmed PR disarmed.
func (c *Client) DisablePullRequestAutoMerge(ctx context.Context, nodeID string) error {
	return c.runPullRequestMutation(ctx, nodeID, "disabling", "auto-merge", disableAutoMergeMutation, "disabled auto-merge on pull request")
}

// runPullRequestMutation sends one of this file's single-id GraphQL mutations
// against a pull request and reports success or failure; the mutation's own
// answer is never decoded, because every call site already knows what it asked for.
func (c *Client) runPullRequestMutation(ctx context.Context, nodeID, verb, state, mutation, successMessage string) error {
	op := fmt.Sprintf("%s pull request %s %s", verb, nodeID, state)
	if nodeID == "" {
		return permanent(op, ErrInvalid, fmt.Errorf("no pull request node id was supplied"))
	}

	body, err := json.Marshal(graphQLRequest{
		Query:     mutation,
		Variables: map[string]any{"id": nodeID},
	})
	if err != nil {
		return fmt.Errorf("%s: encoding the graphql request: %w", op, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: building the graphql request: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/json")

	// c.api.Client() carries installationTransport, the same authentication
	// every REST call on this client uses — GraphQL and REST are one API
	// authenticated one way, and this is the one client that already knows
	// how.
	resp, err := c.api.Client().Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if checked := gh.CheckResponse(resp); checked != nil {
		return classify(ctx, op, checked)
	}

	var decoded graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("%s: decoding the graphql response: %w", op, err)
	}
	if len(decoded.Errors) > 0 {
		return classifyGraphQLErrors(op, decoded.Errors)
	}

	c.log.InfoContext(ctx, successMessage, "node_id", nodeID)
	return nil
}

// classifyGraphQLErrors turns a GraphQL response's own "errors" array into
// this package's vocabulary. GitHub's GraphQL API answers 200 OK for most
// mutation-level failures and reports them here instead of in the HTTP
// status, so ConvertPullRequestToDraft cannot rely on CheckResponse alone to
// see them.
func classifyGraphQLErrors(op string, errs []graphQLError) error {
	for _, e := range errs {
		switch e.Type {
		case "FORBIDDEN", "INSUFFICIENT_SCOPES":
			return permanent(op, ErrAuth, fmt.Errorf("github graphql: %s", e.Message))
		case "NOT_FOUND":
			return permanent(op, ErrNotFound, fmt.Errorf("github graphql: %s", e.Message))
		case "RATE_LIMITED":
			return rateLimited(op, defaultRetryAfter, fmt.Errorf("github graphql: %s", e.Message))
		}
	}
	// Anything else — including GitHub's own transient "SERVICE_UNAVAILABLE" —
	// is left unmarked and therefore retryable, matching classify's own
	// default for an unrecognised REST failure.
	return fmt.Errorf("%s: github graphql: %s", op, errs[0].Message)
}
