package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v78/github"
	"github.com/google/uuid"
)

// LegacyPullRequest is the cutover-relevant projection of one open PR owned
// by the pre-redesign GitHub-issue workflow.
type LegacyPullRequest struct {
	Number           int
	NodeID           string
	Branch           string
	URL              string
	AutoMergeEnabled bool
}

// LegacyPullRequests returns every open PR whose head follows the exact old
// software-factory/ticket-N/run-id branch identity.
func (c *Client) LegacyPullRequests(ctx context.Context) ([]LegacyPullRequest, error) {
	var result []LegacyPullRequest
	opts := &gh.PullRequestListOptions{State: "open", ListOptions: gh.ListOptions{PerPage: perPage}}
	for {
		prs, response, err := c.api.PullRequests.List(ctx, c.owner, c.repo, opts)
		if err != nil {
			return nil, classify(ctx, "listing legacy factory pull requests", err)
		}
		for _, listed := range prs {
			branch := listed.GetHead().GetRef()
			if !legacyFactoryBranch(branch) {
				continue
			}
			pr, _, err := c.api.PullRequests.Get(ctx, c.owner, c.repo, listed.GetNumber())
			if err != nil {
				return nil, classify(ctx, fmt.Sprintf("reading legacy factory pull request %d", listed.GetNumber()), err)
			}
			result = append(result, LegacyPullRequest{
				Number: pr.GetNumber(), NodeID: pr.GetNodeID(), Branch: branch,
				URL: pr.GetHTMLURL(), AutoMergeEnabled: pr.GetAutoMerge() != nil,
			})
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		opts.Page = response.NextPage
	}
	if result == nil {
		result = []LegacyPullRequest{}
	}
	return result, nil
}

func legacyFactoryBranch(branch string) bool {
	parts := strings.Split(branch, "/")
	if len(parts) != 3 || parts[0] != "software-factory" || parts[2] == "" {
		return false
	}
	digits, ok := strings.CutPrefix(parts[1], "ticket-")
	if !ok || digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return false
	}
	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || number <= 0 {
		return false
	}
	_, err = uuid.Parse(parts[2])
	return err == nil
}
