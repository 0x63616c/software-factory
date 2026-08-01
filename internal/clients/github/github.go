// Package github is this service's whole view of the issue tracker, and the
// only place go-github's types exist. It holds the GitHub App identity: it
// signs the App JWT, exchanges it for installation tokens, and keeps one for
// its own calls while minting separate, narrower, always-fresh ones for
// sandboxes to push with.
package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/golang-jwt/jwt/v5"
	gh "github.com/google/go-github/v78/github"
)

// perPage is GitHub's maximum page size. Every listing here uses it, because
// the cost of a listing is requests, not rows.
const perPage = 100

// Defaults a caller may override.
const (
	// defaultRefreshSkew is how long before expiry an installation token counts
	// as spent. See appAuth.refreshSkew for why it is minutes, not seconds.
	defaultRefreshSkew = 5 * time.Minute

	// defaultTimeout bounds one request. Without it a wedged GitHub hangs an
	// activity until its Temporal timeout instead of failing in seconds.
	defaultTimeout = 30 * time.Second
)

// How much comment body survives a write, and how a trimmed thread is bounded.
const (
	// maxCommentBytes is a defensive cap below GitHub's own limit on a comment
	// body. The renderer bounds its free text at source; this exists so that
	// losing a status tail can never fail an otherwise healthy ticket.
	maxCommentBytes = 60_000

	// truncationNotice tells a reader the body below is not all of it.
	truncationNotice = "\n\n_…status truncated…_"
)

// Client talks to one repository as the www-software-factory-bot GitHub App.
//
// It is safe for concurrent use: the dispatcher polls for tickets while
// in-flight WorkTicket workflows post status, and they share one installation
// token rather than each refreshing their own.
type Client struct {
	owner string
	repo  string
	api   *gh.Client
	auth  *appAuth
	log   *slog.Logger

	// graphqlURL is where the pull request draft-state mutations post:
	// GitHub's production endpoint, or a test stub's when withBaseURL
	// redirected the REST plane too. See graphql.go.
	graphqlURL string
	downloads  *http.Client

	// defaultBranchCache holds the repository's default branch once resolved
	// — see defaultBranch. It never changes without a deploy-time repository
	// setting change, so it is cached for the client's whole lifetime rather
	// than re-read before every pull request.
	defaultBranchMu    sync.Mutex
	defaultBranchCache string
}

// options is the optional half of construction. It is unexported: growth goes
// down into this struct, never out into the constructor's signature.
type options struct {
	httpClient  *http.Client
	baseURL     string
	refreshSkew time.Duration
}

// Option configures optional behaviour.
type Option func(*options)

// WithHTTPClient replaces the HTTP client both auth planes are built from. The
// composition root uses it to set a request timeout or a shared transport.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// withBaseURL aims the client at another GitHub, which in practice means a test
// server. Unexported because its only callers are the tests in this package.
func withBaseURL(raw string) Option {
	return func(o *options) { o.baseURL = raw }
}

// New builds a client and fails if it cannot.
//
// The private key is parsed here rather than on first use: a wrong or truncated
// PEM is a config error, and a config error belongs at startup with a clear
// message, not inside an activity retry an hour later where it reads as a
// GitHub outage.
func New(cfg config.GitHub, clk clock.Clock, log *slog.Logger, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuring the github client: %w", err)
	}

	o := options{
		httpClient:  &http.Client{Timeout: defaultTimeout},
		refreshSkew: defaultRefreshSkew,
	}
	for _, opt := range opts {
		opt(&o)
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing the app's private key (GITHUB_APP_PRIVATE_KEY_PEM_FILE): %w", err)
	}

	base := o.httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	auth := &appAuth{
		appID:          cfg.AppID,
		installationID: cfg.InstallationID,
		key:            key,
		clk:            clk,
		log:            log,
		refreshSkew:    o.refreshSkew,
	}

	// Two clients from one injected base. They share a timeout and a transport
	// but never a RoundTripper: see appAuth.exchange for what happens if they
	// do.
	auth.exchange, err = newGitHubClient(&http.Client{Transport: base, Timeout: o.httpClient.Timeout}, o.baseURL)
	if err != nil {
		return nil, err
	}
	api, err := newGitHubClient(&http.Client{
		Transport: &installationTransport{base: base, auth: auth},
		Timeout:   o.httpClient.Timeout,
	}, o.baseURL)
	if err != nil {
		return nil, err
	}

	// GraphQL and REST are one API authenticated one way; when a test points
	// the REST plane at a stub via withBaseURL, the GraphQL plane follows it
	// to the same stub rather than reaching real GitHub.
	graphqlURL := "https://api.github.com/graphql"
	if o.baseURL != "" {
		graphqlURL = strings.TrimSuffix(o.baseURL, "/") + "/graphql"
	}

	return &Client{
		owner: cfg.Owner, repo: cfg.Repo, api: api, auth: auth, log: log, graphqlURL: graphqlURL,
		downloads: &http.Client{Transport: base, Timeout: o.httpClient.Timeout},
	}, nil
}

// newGitHubClient builds an SDK client, optionally aimed elsewhere. go-github
// requires a trailing slash on BaseURL and misroutes silently without one, so
// the slash is added here rather than trusted to every caller.
func newGitHubClient(hc *http.Client, baseURL string) (*gh.Client, error) {
	client := gh.NewClient(hc)
	if baseURL == "" {
		return client, nil
	}
	parsed, err := url.Parse(strings.TrimSuffix(baseURL, "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("parsing the github base url %q: %w", baseURL, err)
	}
	client.BaseURL = parsed
	return client, nil
}

// PullRequestForBranch returns the open pull request whose head is branch, if
// there is one.
//
// This is how a run learns what already exists on its own branch. It is asked
// of GitHub rather than read out of a stage's own report because that report
// is model output derived from issue text an attacker chose, and a URL taken
// from it is a phishing vector that renders as an autolink (#371). The branch
// is one the worker named from a ticket number and a Temporal RunID, so
// nothing an issue author writes can steer which branch is queried or which
// URL comes back.
//
// Not found is not an error: under the pipeline rewrite (#435), PR ownership
// is workflow code — OpenOrUpdatePullRequest creates on Found: false and edits
// on Found: true, so absence here just picks which of those two happens next.
//
// The URL returned is HTMLURL — the page a human opens — not the API URL.
func (c *Client) PullRequestForBranch(ctx context.Context, branch string) (work.PullRequest, bool, error) {
	op := fmt.Sprintf("looking for an open pull request on %s", branch)

	// Head must be qualified by owner, or GitHub matches branches of the same
	// name in every fork and can answer with someone else's pull request.
	opts := &gh.PullRequestListOptions{
		State:       "open",
		Head:        c.owner + ":" + branch,
		ListOptions: gh.ListOptions{PerPage: perPage},
	}

	prs, _, err := c.api.PullRequests.List(ctx, c.owner, c.repo, opts)
	if err != nil {
		return work.PullRequest{}, false, classify(ctx, op, err)
	}
	if len(prs) == 0 {
		return work.PullRequest{}, false, nil
	}

	// One branch, one open pull request — GitHub does not allow two from the
	// same head. Taking the first is not a guess.
	pr := prs[0]
	if pr.GetNumber() == 0 || pr.GetHTMLURL() == "" || pr.GetNodeID() == "" {
		return work.PullRequest{}, false, fmt.Errorf("%s: github returned a pull request with no number, url or node id", op)
	}

	c.log.Info("found the run's pull request", "branch", branch, "pull_request", pr.GetNumber())
	return pullRequestFromGitHub(pr), true, nil
}

// defaultBranch resolves the repository's default branch — the base every
// pull request this service opens targets — and caches it for the client's
// whole lifetime. It never changes without a deploy-time repository setting
// change, so re-reading it before every pull request would be a wasted round
// trip.
func (c *Client) defaultBranch(ctx context.Context) (string, error) {
	c.defaultBranchMu.Lock()
	defer c.defaultBranchMu.Unlock()

	if c.defaultBranchCache != "" {
		return c.defaultBranchCache, nil
	}

	repo, _, err := c.api.Repositories.Get(ctx, c.owner, c.repo)
	if err != nil {
		return "", classify(ctx, "reading the repository's default branch", err)
	}
	if repo.GetDefaultBranch() == "" {
		return "", fmt.Errorf("reading the repository's default branch: github returned none")
	}

	c.defaultBranchCache = repo.GetDefaultBranch()
	return c.defaultBranchCache, nil
}

// OpenOrUpdatePullRequest creates the run's pull request the first time its
// branch has anything pushed, and edits it in place on every push after that
// whose title or body actually changed. existing is what a prior
// PullRequestForBranch call already found on this branch — nil means none
// exists yet — so this never re-queries what its caller already knows.
//
// PR ownership moved here from the model (#435): `propose` used to run
// `gh pr create` itself, from inside the sandbox, once, at the end of a fixed
// pipeline. Under the implement/review loop this opens the pull request after
// the FIRST successful push and is never held back waiting for CI or review,
// so a human watching the ticket sees a diff the moment there is one.
func (c *Client) OpenOrUpdatePullRequest(ctx context.Context, branch, title, body string, existing *work.PullRequest) (work.PullRequest, error) {
	if existing == nil {
		return c.createPullRequest(ctx, branch, title, body)
	}
	if existing.Title == title && existing.Body == body {
		// Idempotent no-op: a push that changed nothing implement/review
		// hadn't already told GitHub about must not spend an Edit call it
		// does not need.
		return *existing, nil
	}
	return c.editPullRequest(ctx, *existing, title, body)
}

// createPullRequest opens a new pull request from branch onto the
// repository's default branch.
func (c *Client) createPullRequest(ctx context.Context, branch, title, body string) (work.PullRequest, error) {
	op := fmt.Sprintf("opening a pull request from %s", branch)

	base, err := c.defaultBranch(ctx)
	if err != nil {
		return work.PullRequest{}, err
	}

	pr, _, err := c.api.PullRequests.Create(ctx, c.owner, c.repo, &gh.NewPullRequest{
		Title: gh.Ptr(title),
		Body:  gh.Ptr(body),
		Head:  gh.Ptr(branch),
		Base:  gh.Ptr(base),
		Draft: gh.Ptr(true),
	})
	if err != nil {
		return work.PullRequest{}, classify(ctx, op, err)
	}
	if pr.GetNumber() == 0 || pr.GetHTMLURL() == "" || pr.GetNodeID() == "" || !pr.GetDraft() {
		return work.PullRequest{}, fmt.Errorf("%s: github returned a pull request with no number, url, node id or draft state", op)
	}

	c.log.InfoContext(ctx, "opened the run's pull request", "branch", branch, "pull_request", pr.GetNumber())
	return pullRequestFromGitHub(pr), nil
}

// editPullRequest rewrites an existing pull request's title and body.
func (c *Client) editPullRequest(ctx context.Context, existing work.PullRequest, title, body string) (work.PullRequest, error) {
	op := fmt.Sprintf("editing pull request #%d", existing.Number)

	pr, _, err := c.api.PullRequests.Edit(ctx, c.owner, c.repo, existing.Number, &gh.PullRequest{
		Title: gh.Ptr(title),
		Body:  gh.Ptr(body),
	})
	if err != nil {
		return work.PullRequest{}, classify(ctx, op, err)
	}

	c.log.InfoContext(ctx, "edited the run's pull request", "pull_request", existing.Number)
	updated := pullRequestFromGitHub(pr)
	updated.Number = existing.Number
	updated.URL = existing.URL
	updated.NodeID = existing.NodeID
	if updated.State == "" {
		updated.State = existing.State
	}
	if updated.HeadSHA == "" {
		updated.HeadSHA = existing.HeadSHA
	}
	if updated.BaseSHA == "" {
		updated.BaseSHA = existing.BaseSHA
	}
	if updated.MergeSHA == "" {
		updated.MergeSHA = existing.MergeSHA
	}
	return updated, nil
}

func pullRequestFromGitHub(pr *gh.PullRequest) work.PullRequest {
	return work.PullRequest{
		Number:       pr.GetNumber(),
		URL:          pr.GetHTMLURL(),
		State:        restPullRequestState(pr.GetState()),
		HeadSHA:      pr.GetHead().GetSHA(),
		BaseSHA:      pr.GetBase().GetSHA(),
		Mergeability: restMergeability(pr.GetMergeableState()),
		MergeSHA:     pr.GetMergeCommitSHA(),
		Draft:        pr.GetDraft(),
		NodeID:       pr.GetNodeID(),
		Title:        pr.GetTitle(),
		Body:         pr.GetBody(),
	}
}

func restPullRequestState(state string) work.PullRequestState {
	switch state {
	case "open":
		return work.PullRequestStateOpen
	case "closed":
		return work.PullRequestStateClosed
	default:
		return ""
	}
}

func restMergeability(state string) work.PullRequestMergeability {
	switch state {
	case "clean":
		return work.PullRequestMergeabilityMergeable
	case "dirty":
		return work.PullRequestMergeabilityConflicting
	default:
		return work.PullRequestMergeabilityUnknown
	}
}

// PostComment adds a comment to an issue or pull request. Pull requests use
// GitHub's issues endpoint for comments, so number is the resource's shared
// number in either case.
func (c *Client) PostComment(ctx context.Context, number int, body string) error {
	op := fmt.Sprintf("posting a comment on #%d", number)

	comment, _, err := c.api.Issues.CreateComment(ctx, c.owner, c.repo, number, &gh.IssueComment{Body: gh.Ptr(capBody(body))})
	if err != nil {
		return classify(ctx, op, err)
	}
	c.log.InfoContext(ctx, "posted a comment", "number", number, "comment_id", comment.GetID())
	return nil
}

// InstallationToken mints a fresh, repository-scoped token for a sandbox to
// push with.
//
// It deliberately bypasses the cache. The cached token has an arbitrary
// remaining lifetime — possibly three minutes — while the implement stage
// pushes a branch up to an hour later.
func (c *Client) InstallationToken(ctx context.Context) (work.GitHubCredential, error) {
	const op = "minting a repository-scoped installation token for the sandbox"

	// Resolved before the token is minted, deliberately: this is a cached read
	// after the first call, and a failure here must not leave a live token
	// minted for a sandbox that never receives it. See work.GitHubCredential
	// for why gh cannot proceed without it.
	identity, err := c.auth.attribution(ctx)
	if err != nil {
		return work.GitHubCredential{}, err
	}

	token, expiresAt, err := c.auth.mint(ctx, op, &gh.InstallationTokenOptions{
		Repositories: []string{c.repo},
		Permissions: &gh.InstallationPermissions{
			// implement clones and pushes the branch.
			Contents: gh.Ptr("write"),
			// A push touching .github/workflows is rejected at the git layer
			// without this, with an error that never reaches this client's
			// taxonomy. Agents edit workflows, so this is not hypothetical.
			Workflows: gh.Ptr("write"),
			// OpenOrUpdatePullRequest and the draft-state mutations need it.
			PullRequests: gh.Ptr("write"),
			// GitHub will not grant the others without it.
			Metadata: gh.Ptr("read"),
		},
	})
	if err != nil {
		return work.GitHubCredential{}, err
	}

	// Note what is absent: issues:write, because the WORKER posts status and
	// clears the label — the sandbox runs agent-authored code and has no
	// business writing to the issue — and actions/checks/statuses, because
	// nothing in this pipeline reruns or watches CI.
	c.log.InfoContext(ctx, "minted a repository-scoped installation token for a sandbox",
		"repository", c.repo, "login", identity.Login, "account_id", identity.AccountID)
	return work.GitHubCredential{Token: work.NewCredential(token), Login: identity.Login, AccountID: identity.AccountID, ExpiresAt: expiresAt}, nil
}

// capBody bounds a comment body at a rune boundary.
func capBody(body string) string {
	if len(body) <= maxCommentBytes {
		return body
	}
	cut := body[:maxCommentBytes-len(truncationNotice)]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + truncationNotice
}
