package github

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	gh "github.com/google/go-github/v78/github"
)

type projectedCredentialTransport struct {
	path     string
	readFile func(string) ([]byte, error)
	next     http.RoundTripper
}

func (t projectedCredentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	raw, err := t.readFile(t.path)
	if err != nil {
		return nil, fmt.Errorf("reading projected GitHub credential: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("reading projected GitHub credential: file is empty")
	}
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+token)
	return t.next.RoundTrip(copy)
}

// NewProjected builds the target-side GitHub client. Its transport reopens the
// projected token for every request, so Kubernetes rotation is observed
// without keeping a token in this process's environment or client state.
func NewProjected(owner, repo, tokenPath string, readFile func(string) ([]byte, error), logger *slog.Logger) (*Client, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return nil, fmt.Errorf("constructing projected GitHub client: owner and repository are required")
	}
	if !filepath.IsAbs(tokenPath) {
		return nil, fmt.Errorf("constructing projected GitHub client: token path must be absolute")
	}
	if readFile == nil || logger == nil {
		return nil, fmt.Errorf("constructing projected GitHub client: file reader and logger are required")
	}
	transport := projectedCredentialTransport{path: tokenPath, readFile: readFile, next: http.DefaultTransport}
	httpClient := &http.Client{Transport: transport, Timeout: defaultTimeout}
	return &Client{
		owner: owner, repo: repo, api: gh.NewClient(httpClient), log: logger,
		graphqlURL: "https://api.github.com/graphql", downloads: httpClient,
	}, nil
}
