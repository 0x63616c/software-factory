package github

import (
	"context"
	"fmt"

	"github.com/0x63616c/software-factory/internal/work"
	gh "github.com/google/go-github/v78/github"
)

// RetirePullRequest closes an unmerged predecessor pull request before a
// successor Run creates its own. It is idempotent and never closes a merge.
func (c *Client) RetirePullRequest(ctx context.Context, number int) (work.PullRequestRetirement, error) {
	if number <= 0 {
		return work.PullRequestRetirement{}, fmt.Errorf("retiring pull request: pull request number is required")
	}
	state, err := c.pullRequestMergeState(ctx, number)
	if err != nil {
		return work.PullRequestRetirement{}, fmt.Errorf("reading pull request #%d before retirement: %w", number, err)
	}
	if state.Merged {
		return retirementFromMergeState(state)
	}
	if state.State == "CLOSED" {
		return work.PullRequestRetirement{}, nil
	}
	if state.State != "OPEN" {
		return work.PullRequestRetirement{}, fmt.Errorf("retiring pull request #%d: unexpected state %q", number, state.State)
	}
	if _, _, err := c.api.PullRequests.Edit(ctx, c.owner, c.repo, number, &gh.PullRequest{State: gh.Ptr("closed")}); err != nil {
		return work.PullRequestRetirement{}, classify(ctx, fmt.Sprintf("closing canceled pull request #%d", number), err)
	}
	confirmed, err := c.pullRequestMergeState(ctx, number)
	if err != nil {
		return work.PullRequestRetirement{}, fmt.Errorf("confirming retirement of pull request #%d: %w", number, err)
	}
	if confirmed.Merged {
		return retirementFromMergeState(confirmed)
	}
	return work.PullRequestRetirement{}, nil
}

func retirementFromMergeState(state graphQLPullRequest) (work.PullRequestRetirement, error) {
	if state.MergeCommit == nil || state.MergeCommit.SHA == "" || state.HeadSHA == "" {
		return work.PullRequestRetirement{}, fmt.Errorf("reading merged pull request: GitHub omitted head or merge commit")
	}
	return work.PullRequestRetirement{Merged: true, ReviewedHead: state.HeadSHA, MergeSHA: state.MergeCommit.SHA}, nil
}
