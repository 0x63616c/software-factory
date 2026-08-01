package github

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestProjectedCredentialTransportReadsTheCurrentTokenForEveryRequest(t *testing.T) {
	tokens := []string{"first-token", "rotated-token"}
	reads := 0
	seen := []string{}
	transport := projectedCredentialTransport{
		path: "/projected/token",
		readFile: func(string) ([]byte, error) {
			value := tokens[reads]
			reads++
			return []byte(value + "\n"), nil
		},
		next: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seen = append(seen, req.Header.Get("Authorization"))
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
		}),
	}
	client := &http.Client{Transport: transport}
	for range 2 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.com/user", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if reads != 2 || len(seen) != 2 || seen[0] != "Bearer first-token" || seen[1] != "Bearer rotated-token" {
		t.Fatalf("reads/headers = %d / %#v", reads, seen)
	}
}

func TestNewProjectedRejectsInvalidNonSecretConfiguration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, input := range [][3]string{{"", "repo", "/token"}, {"owner", "", "/token"}, {"owner", "repo", "relative"}} {
		if _, err := NewProjected(input[0], input[1], input[2], func(string) ([]byte, error) { return []byte("token"), nil }, logger); err == nil {
			t.Fatalf("NewProjected accepted owner=%q repo=%q path=%q", input[0], input[1], input[2])
		}
	}
}
