package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/golang-jwt/jwt/v5"
)

// listPulls registers an empty pull request list, the cheapest API call a test
// can make to observe the auth planes underneath it.
func listPulls(s *stub) {
	s.handle(fmt.Sprintf("GET /repos/%s/%s/pulls", testOwner, testRepo), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []any{})
	})
}

// anAuthenticatedCall makes one ordinary authenticated request, so a test can
// assert on the auth planes underneath it without caring which endpoint it is.
func anAuthenticatedCall(c *Client) error {
	_, _, err := c.PullRequestForBranch(context.Background(), "a-branch")
	return err
}

func TestMintsAnAppJWTWithTheIssuerAndAWindowHeldOffGitHubsLimitsAtBothEnds(t *testing.T) {
	t.Parallel()

	s, clk := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("an authenticated call returned an unexpected error: %v", err)
	}

	raw := strings.TrimPrefix(s.first(t, fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)).Auth, "Bearer ")
	claims := &jwt.RegisteredClaims{}
	// Validated against the fake clock: the claims are minted from it, and the
	// real clock is somewhere else entirely.
	token, err := jwt.ParseWithClaims(raw, claims,
		func(*jwt.Token) (any, error) { return &testKey().PublicKey, nil },
		jwt.WithTimeFunc(clk.Now))
	if err != nil {
		t.Fatalf("the app jwt did not parse: %v", err)
	}
	if got := token.Method.Alg(); got != "RS256" {
		t.Errorf("app jwt alg = %s, want RS256", got)
	}
	if want := fmt.Sprint(testAppID); claims.Issuer != want {
		t.Errorf("app jwt iss = %q, want the app id %q", claims.Issuer, want)
	}
	// Both ends are pulled a minute inward, because GitHub validates them
	// against ITS clock and pod clock skew is real. Pinned as literals rather
	// than as the constants they come from: a test that restates the code
	// cannot catch the code changing.
	if want := clk.Now().Add(-60 * time.Second); !claims.IssuedAt.Equal(want) {
		t.Errorf("app jwt iat = %s, want %s — backdated, or github reads it as issued in its future",
			claims.IssuedAt, want)
	}
	if want := clk.Now().Add(9 * time.Minute); !claims.ExpiresAt.Equal(want) {
		t.Errorf("app jwt exp = %s, want %s — a minute inside github's ten-minute ceiling, or a fast local clock 401s",
			claims.ExpiresAt, want)
	}
	if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got > 10*time.Minute {
		t.Errorf("app jwt exp - iat = %s, want no more than github's maximum of 10m", got)
	}
}

func TestSignsTheAppJWTWithTheConfiguredPrivateKey(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("an authenticated call returned an unexpected error: %v", err)
	}
	raw := strings.TrimPrefix(s.first(t, fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)).Auth, "Bearer ")

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a second key: %v", err)
	}
	_, err = jwt.Parse(raw, func(*jwt.Token) (any, error) { return &other.PublicKey, nil }, jwt.WithTimeFunc(s.clk.Now))
	if err == nil {
		t.Error("the app jwt verified against a key that did not sign it")
	}
}

func TestExchangesTheAppJWTForAnInstallationToken(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("an authenticated call returned an unexpected error: %v", err)
	}

	api := s.first(t, fmt.Sprintf("GET /repos/%s/%s/pulls", testOwner, testRepo))
	if want := "Bearer installation-token-1"; api.Auth != want {
		t.Errorf("api call Authorization = %q, want the exchanged token %q", api.Auth, want)
	}
}

func TestReusesTheCachedInstallationTokenUntilTheRefreshSkew(t *testing.T) {
	t.Parallel()

	s, clk := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("first authenticated call: %v", err)
	}
	clk.Advance(30 * time.Minute)
	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("second authenticated call: %v", err)
	}

	if got := s.count(fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)); got != 1 {
		t.Errorf("exchanged the app jwt %d times, want 1 — the cached token was still good", got)
	}
}

func TestRefreshesTheInstallationTokenOnceItIsInsideTheRefreshSkew(t *testing.T) {
	t.Parallel()

	s, clk := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("first authenticated call: %v", err)
	}
	// Four minutes short of expiry, inside the five-minute skew.
	clk.Advance(56 * time.Minute)
	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("second authenticated call: %v", err)
	}

	if got := s.count(fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)); got != 2 {
		t.Fatalf("exchanged the app jwt %d times, want 2", got)
	}
	calls := 0
	for _, r := range s.requests() {
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/issues") {
			calls++
			if calls == 2 && r.Auth != "Bearer installation-token-2" {
				t.Errorf("second api call Authorization = %q, want the refreshed token", r.Auth)
			}
		}
	}
}

func TestRefreshesAnInstallationTokenThatHasAlreadyExpired(t *testing.T) {
	t.Parallel()

	s, clk := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("first authenticated call: %v", err)
	}
	clk.Advance(2 * time.Hour)
	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("second authenticated call: %v", err)
	}

	if got := s.count(fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)); got != 2 {
		t.Errorf("exchanged the app jwt %d times, want 2", got)
	}
}

func TestServesOneRefreshToConcurrentCallers(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := anAuthenticatedCall(c); err != nil {
				t.Errorf("an authenticated call returned an unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := s.count(fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)); got != 1 {
		t.Errorf("exchanged the app jwt %d times for 16 concurrent callers, want 1", got)
	}
}

func TestDoesNotCacheAFailedExchange(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	failures := 0
	exchange := s.exchange
	s.exchange = func(w http.ResponseWriter, r *http.Request) {
		if failures == 0 {
			failures++
			writeError(w, http.StatusInternalServerError, "server error")
			return
		}
		exchange(w, r)
	}
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err == nil {
		t.Fatal("the authenticated call succeeded despite a failed token exchange")
	}
	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("second authenticated call: %v", err)
	}
	if got := s.count(fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)); got != 2 {
		t.Errorf("exchanged the app jwt %d times, want 2 — a failed exchange must cache nothing", got)
	}
}

func TestNewFailsWhenThePrivateKeyIsNotAUsablePEM(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.PrivateKeyPEM = []byte("this is not a pem")

	c, err := New(cfg, clocktest.NewFake(testStart), slogDiscard(), withBaseURL("http://127.0.0.1:1"))
	if err == nil {
		t.Fatal("New accepted a private key that is not a pem")
	}
	if c != nil {
		t.Error("New returned a client alongside an error")
	}
	if !strings.Contains(err.Error(), "private key") {
		t.Errorf("error %q does not say which input is unusable", err)
	}
}

func TestNewFailsWhenARequiredConfigFieldIsMissing(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*config.GitHub){
		"owner":           func(c *config.GitHub) { c.Owner = "" },
		"repo":            func(c *config.GitHub) { c.Repo = "" },
		"app id":          func(c *config.GitHub) { c.AppID = 0 },
		"installation id": func(c *config.GitHub) { c.InstallationID = 0 },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			breakIt(&cfg)
			if _, err := New(cfg, clocktest.NewFake(testStart), slogDiscard()); err == nil {
				t.Fatalf("New accepted a config with no %s", name)
			}
		})
	}
}

func TestReportsARejectedAppJWTAsAPermanentAuthFailure(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	s.exchange = func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
	}
	c, _ := s.client(t)

	err := anAuthenticatedCall(c)
	assertPermanent(t, err, ErrAuth)
}

func TestReportsAMissingInstallationAsAPermanentAuthFailure(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	s.exchange = func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "Not Found")
	}
	c, _ := s.client(t)

	err := anAuthenticatedCall(c)
	assertPermanent(t, err, ErrAuth)
}

func TestNeverLogsAToken(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	c, logs := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("an authenticated call returned an unexpected error: %v", err)
	}
	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("InstallationToken returned an unexpected error: %v", err)
	}

	for _, secret := range append([]string{"BEGIN RSA PRIVATE KEY"}, s.tokens...) {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the logs contain %q", secret)
		}
	}
	// The app jwt is minted per exchange, so match its shape rather than its
	// value.
	if strings.Contains(logs.String(), "eyJ") {
		t.Errorf("the logs contain something jwt-shaped: %s", logs)
	}
}

func TestDoesNotAuthenticateTheTokenExchangeWithAnInstallationToken(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	listPulls(s)
	c, _ := s.client(t)

	if err := anAuthenticatedCall(c); err != nil {
		t.Fatalf("an authenticated call returned an unexpected error: %v", err)
	}

	exchangePath := fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID)
	if got := s.count(exchangePath); got != 1 {
		// Routing the exchange through the transport that performs it recurses,
		// and deadlocks on the mutex the refresh already holds.
		t.Fatalf("exchanged the app jwt %d times for one api call, want 1", got)
	}
	auth := s.first(t, exchangePath).Auth
	for _, token := range s.tokens {
		if strings.Contains(auth, token) {
			t.Fatalf("the token exchange authenticated with an installation token (%q)", auth)
		}
	}
	if !strings.HasPrefix(auth, "Bearer eyJ") {
		t.Errorf("the token exchange Authorization = %q, want the app jwt", auth)
	}
}

// assertPermanent checks an error is the given kind and is one a retry cannot
// fix, which is the single bit internal/activities translates into Temporal's
// taxonomy.
func assertPermanent(t *testing.T, err error, kind error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %v failure, got none", kind)
	}
	if !errors.Is(err, kind) {
		t.Errorf("error %q is not %v", err, kind)
	}
	if !errors.Is(err, work.ErrPermanent) {
		t.Errorf("error %q is retryable; a retry cannot fix it", err)
	}
}

// assertRetryable checks an error is left retryable, which is Temporal's
// default for anything unmarked.
func assertRetryable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a failure, got none")
	}
	if errors.Is(err, work.ErrPermanent) {
		t.Errorf("error %q is marked permanent; a retry could have fixed it", err)
	}
}
