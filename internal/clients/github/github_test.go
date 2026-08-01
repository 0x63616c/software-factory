package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// Paths every test in this file addresses, spelled once.
var (
	issuesPath   = fmt.Sprintf("/repos/%s/%s/issues", testOwner, testRepo)
	issuePath    = fmt.Sprintf("%s/%d", issuesPath, testIssue)
	commentsPath = issuePath + "/comments"
	pullsPath    = fmt.Sprintf("/repos/%s/%s/pulls", testOwner, testRepo)
	exchangePath = fmt.Sprintf("/app/installations/%d/access_tokens", testInstallationID)
)

// comment builds the JSON GitHub returns for one issue comment.
func comment(id int64, author, body string) map[string]any {
	return map[string]any{"id": id, "body": body, "user": map[string]any{"login": author}}
}

// TestPostsACommentOnAnIssueOrPullRequest is the whole of what this client
// still writes to a thread, now that the GitHub-backed pipeline's status
// comments are gone (#559): one comment, no marker, no adoption, no edit.
func TestPostsACommentOnAnIssueOrPullRequest(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("POST "+commentsPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusCreated, comment(999, testBotLogin, "posted"))
	})
	c, _ := s.client(t)

	if err := c.PostComment(context.Background(), testIssue, "a comment"); err != nil {
		t.Fatalf("PostComment returned an unexpected error: %v", err)
	}
	if got := s.count("GET " + commentsPath); got != 0 {
		t.Errorf("listed comments %d times, want 0 — posting a comment must not page the thread", got)
	}
	sent := decodeBody(t, s.first(t, "POST "+commentsPath))
	if sent["body"] != "a comment" || len(sent) != 1 {
		t.Errorf("posted %v, want a body-only request", sent)
	}
}

func TestTruncatesAnOversizedCommentBody(t *testing.T) {
	t.Parallel()

	oversized := strings.Repeat("x", 200_000)

	s, _ := newStub(t)
	s.handle("POST "+commentsPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusCreated, comment(999, testBotLogin, "posted"))
	})
	c, _ := s.client(t)

	if err := c.PostComment(context.Background(), testIssue, oversized); err != nil {
		t.Fatalf("PostComment returned an unexpected error: %v", err)
	}
	assertCapped(t, decodeBody(t, s.first(t, "POST "+commentsPath))["body"])
}

func TestTruncatesOnARuneBoundary(t *testing.T) {
	t.Parallel()

	// Three-byte runes, sized so the cap lands mid-rune.
	oversized := strings.Repeat("→", maxCommentBytes)

	s, _ := newStub(t)
	s.handle("POST "+commentsPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusCreated, comment(999, testBotLogin, "posted"))
	})
	c, _ := s.client(t)

	if err := c.PostComment(context.Background(), testIssue, oversized); err != nil {
		t.Fatalf("PostComment returned an unexpected error: %v", err)
	}
	sent, _ := decodeBody(t, s.first(t, "POST "+commentsPath))["body"].(string)
	if !utf8.ValidString(sent) {
		t.Error("the truncated body is not valid utf-8; the cap cut a rune in half")
	}
	assertCapped(t, sent)
}

// assertCapped checks a written body was bounded and says so.
func assertCapped(t *testing.T, sent any) {
	t.Helper()
	body, ok := sent.(string)
	if !ok {
		t.Fatalf("body = %v, want a string", sent)
	}
	if len(body) > maxCommentBytes {
		t.Errorf("body is %d bytes, want at most %d", len(body), maxCommentBytes)
	}
	if !strings.HasSuffix(body, truncationNotice) {
		t.Error("the truncated body does not say it was truncated")
	}
}

func TestMintsARepositoryScopedTokenCarryingEveryPermissionTheSandboxNeeds(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	c, _ := s.client(t)

	cred, err := c.InstallationToken(context.Background())
	if err != nil {
		t.Fatalf("InstallationToken returned an unexpected error: %v", err)
	}
	if cred.Token.Reveal() != "installation-token-1" {
		t.Errorf("credential = %q, want the exchanged token", cred.Token.Reveal())
	}
	if cred.Login != testBotLogin {
		t.Errorf("credential login = %q, want %q", cred.Login, testBotLogin)
	}
	if cred.AccountID != testBotAccountID {
		t.Errorf("credential account ID = %d, want %d", cred.AccountID, testBotAccountID)
	}
	if auth := s.first(t, "GET /app").Auth; !strings.HasPrefix(auth, "Bearer eyJ") {
		t.Errorf("GET /app Authorization = %q, want the app jwt", auth)
	}
	if auth := s.first(t, "GET /users/"+testBotLogin).Auth; auth != "" {
		t.Errorf("GET /users/%s Authorization = %q, want no authentication", testBotLogin, auth)
	}

	sent := decodeBody(t, s.first(t, "POST "+exchangePath))
	repos, _ := sent["repositories"].([]any)
	if len(repos) != 1 || repos[0] != testRepo {
		t.Errorf("repositories = %v, want exactly [%s]", sent["repositories"], testRepo)
	}

	granted, _ := sent["permissions"].(map[string]any)
	want := map[string]any{
		"contents":      "write",
		"workflows":     "write",
		"pull_requests": "write",
		"metadata":      "read",
	}
	for name, level := range want {
		if granted[name] != level {
			// workflows:write in particular: without it a push touching
			// .github/workflows is rejected at the git layer, with an error
			// that never reaches this client's taxonomy.
			t.Errorf("permission %s = %v, want %v", name, granted[name], level)
		}
	}
	if len(granted) != len(want) {
		t.Errorf("permissions = %v, want exactly %v", granted, want)
	}
}

func TestCachesBotAttributionButMintsAFreshSandboxToken(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	c, _ := s.client(t)

	for range 2 {
		if _, err := c.InstallationToken(context.Background()); err != nil {
			t.Fatalf("InstallationToken returned an unexpected error: %v", err)
		}
	}
	if got := s.count("GET /app"); got != 1 {
		t.Errorf("GET /app count = %d, want 1", got)
	}
	if got := s.count("GET /users/" + testBotLogin); got != 1 {
		t.Errorf("GET /users/%s count = %d, want 1", testBotLogin, got)
	}
	if got := s.count("POST " + exchangePath); got != 2 {
		t.Errorf("sandbox token exchanges = %d, want 2", got)
	}
}

func TestDoesNotMintASandboxTokenWhenBotProfileLookupFails(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.userGet = func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, "server error")
	}
	c, _ := s.client(t)

	if _, err := c.InstallationToken(context.Background()); err == nil {
		t.Fatal("InstallationToken succeeded despite a failed bot profile lookup")
	}
	if got := s.count("POST " + exchangePath); got != 0 {
		t.Errorf("sandbox token exchanges = %d, want 0 after profile lookup failure", got)
	}
	if login, err := c.auth.botLogin(context.Background()); err != nil || login != testBotLogin {
		t.Errorf("botLogin = %q, %v; want cached login %q and no error", login, err, testBotLogin)
	}
}

func TestRejectsMalformedBotProfilesBeforeMintingASandboxToken(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]any{
		"zero account id":  {"id": 0, "login": testBotLogin},
		"empty login":      {"id": testBotAccountID, "login": ""},
		"mismatched login": {"id": testBotAccountID, "login": "some-other-bot[bot]"},
	}
	for name, profile := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newStub(t)
			s.userGet = func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, profile)
			}
			c, _ := s.client(t)

			if _, err := c.InstallationToken(context.Background()); err == nil {
				t.Fatal("InstallationToken succeeded despite a malformed bot profile")
			}
			if got := s.count("POST " + exchangePath); got != 0 {
				t.Errorf("sandbox token exchanges = %d, want 0 after malformed profile", got)
			}
		})
	}
}

func TestDoesNotGrantTheSandboxIssuesWrite(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	c, _ := s.client(t)

	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("InstallationToken returned an unexpected error: %v", err)
	}

	granted, _ := decodeBody(t, s.first(t, "POST "+exchangePath))["permissions"].(map[string]any)
	for _, name := range []string{"issues", "actions", "checks", "statuses"} {
		if _, present := granted[name]; present {
			t.Errorf("the sandbox token requests %s; the worker writes to the issue, not the sandbox", name)
		}
	}
}

func TestReportsAnUngrantedPermissionAsAnAuthFailureNamingTheMissingGrant(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.exchange = func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusUnprocessableEntity, "The permissions requested are not granted to this installation.")
	}
	c, _ := s.client(t)

	_, err := c.InstallationToken(context.Background())
	assertPermanent(t, err, ErrAuth)
	if !strings.Contains(err.Error(), "pending permission request") {
		t.Errorf("error %q does not point at approving the pending grant", err)
	}
}

func TestDoesNotHandTheSandboxTheCachedToken(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("an authenticated call returned an unexpected error: %v", err)
	}
	cred, err := c.InstallationToken(context.Background())
	if err != nil {
		t.Fatalf("InstallationToken returned an unexpected error: %v", err)
	}

	if got := s.count("POST " + exchangePath); got != 2 {
		t.Errorf("exchanged %d times, want 2 — the sandbox gets a fresh full-hour token", got)
	}
	if cred.Token.Reveal() != "installation-token-2" {
		t.Errorf("the sandbox got %q, want the freshly minted token", cred.Token.Reveal())
	}
}

func TestReturnsACredentialThatRedactsItself(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	c, _ := s.client(t)

	cred, err := c.InstallationToken(context.Background())
	if err != nil {
		t.Fatalf("InstallationToken returned an unexpected error: %v", err)
	}
	if got := fmt.Sprintf("%v", cred); got != "[redacted]" {
		t.Errorf("a stray %%v printed %q", got)
	}
}
