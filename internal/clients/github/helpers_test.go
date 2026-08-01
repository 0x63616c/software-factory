package github

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/config"
)

// The repository and App every test addresses. Fixed values, so an assertion on
// a request path reads as the path the worker will really build.
const (
	testOwner          = "0x63616c"
	testRepo           = "world-wide-webb"
	testAppID          = 1234567
	testInstallationID = 89012345
	testBotSlug        = "www-software-factory-bot"
	testBotLogin       = testBotSlug + "[bot]"
	testBotAccountID   = int64(309464436)
	testIssue          = 328
)

// testStart is the instant every fake clock starts at. UTC, because the service
// is.
var testStart = time.Date(2026, 7, 28, 16, 14, 22, 0, time.UTC)

// testKey is generated once per test binary rather than checked in: a file
// shaped like a private key trips secret scanners and reads to a human as a
// live credential, whichever comment sits above it.
var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("generating a test key: " + err.Error())
	}
	return key
})

// testConfig is a valid config pointing at the generated key.
func testConfig() config.GitHub {
	return config.GitHub{
		Owner:          testOwner,
		Repo:           testRepo,
		AppID:          testAppID,
		InstallationID: testInstallationID,
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(testKey()),
		}),
	}
}

// recorded is one request the stub saw, kept after its body is consumed.
type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Auth   string
	Body   []byte
}

// stub is a fake GitHub, served over loopback.
//
// The tests drive a real HTTP round trip rather than an interface fake because
// the behaviour under test IS wire behaviour: the query the list builds, Link
// pagination, the exact body of a token exchange, and which status code maps to
// which verdict. A fake beneath go-github would assert that we call our own
// fake and leave every one of those untested.
type stub struct {
	URL string

	clk *clocktest.Fake
	mux *http.ServeMux

	mu   sync.Mutex
	seen []recorded

	// exchange and appGet answer the two App-level endpoints every client
	// touches. They are fields rather than routes so a test can replace one
	// without re-registering a pattern.
	exchange http.HandlerFunc
	appGet   http.HandlerFunc
	userGet  http.HandlerFunc

	// tokens are handed out by the default exchange in order, so a test can
	// tell a refreshed token from the one it replaced.
	tokens        []string
	exchanges     int
	tokenLifetime time.Duration
}

func newStub(t *testing.T) (*stub, *clocktest.Fake) {
	t.Helper()

	clk := clocktest.NewFake(testStart)
	s := &stub{
		clk:           clk,
		mux:           http.NewServeMux(),
		tokens:        []string{"installation-token-1", "installation-token-2", "installation-token-3"},
		tokenLifetime: time.Hour,
	}
	s.exchange = s.defaultExchange
	s.appGet = s.defaultAppGet
	s.userGet = s.defaultUserGet

	s.mux.HandleFunc(fmt.Sprintf("POST /app/installations/%d/access_tokens", testInstallationID),
		func(w http.ResponseWriter, r *http.Request) { s.exchange(w, r) })
	s.mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) { s.appGet(w, r) })
	s.mux.HandleFunc("GET /users/"+testBotLogin, func(w http.ResponseWriter, r *http.Request) { s.userGet(w, r) })

	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s, clk
}

// ServeHTTP records the request, then routes it.
func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))

	s.mu.Lock()
	s.seen = append(s.seen, recorded{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Auth:   r.Header.Get("Authorization"),
		Body:   body,
	})
	s.mu.Unlock()

	s.mux.ServeHTTP(w, r)
}

// handle installs a route, using Go's method-aware pattern syntax
// ("GET /repos/owner/repo/issues").
func (s *stub) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h)
}

func (s *stub) defaultExchange(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	token := s.tokens[min(s.exchanges, len(s.tokens)-1)]
	s.exchanges++
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"expires_at": s.clk.Now().Add(s.tokenLifetime).Format(time.RFC3339),
	})
}

func (s *stub) defaultAppGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": testAppID, "slug": testBotSlug})
}

func (s *stub) defaultUserGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": testBotAccountID, "login": testBotLogin})
}

// requests returns every request the stub saw, in order.
func (s *stub) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.seen...)
}

// count returns how many requests matched "METHOD /path".
func (s *stub) count(methodAndPath string) int {
	method, path, _ := strings.Cut(methodAndPath, " ")
	n := 0
	for _, r := range s.requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// first returns the first request matching "METHOD /path", failing the test if
// there is none.
func (s *stub) first(t *testing.T, methodAndPath string) recorded {
	t.Helper()
	method, path, _ := strings.Cut(methodAndPath, " ")
	for _, r := range s.requests() {
		if r.Method == method && r.Path == path {
			return r
		}
	}
	t.Fatalf("no %s request was made; saw %s", methodAndPath, s)
	return recorded{}
}

// String lists what the stub saw, so a failure says what happened instead of
// only what did not.
func (s *stub) String() string {
	var b strings.Builder
	for _, r := range s.requests() {
		fmt.Fprintf(&b, "\n  %s %s?%s", r.Method, r.Path, r.Query.Encode())
	}
	return b.String()
}

// client builds a Client aimed at this stub, with logs captured.
func (s *stub) client(t *testing.T) (*Client, *bytes.Buffer) {
	return s.clientWithOptions(t)
}

// clientWithOptions builds the same captured client while letting a focused
// transport test inject behaviour below go-github's request machinery.
func (s *stub) clientWithOptions(t *testing.T, opts ...Option) (*Client, *bytes.Buffer) {
	t.Helper()

	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	opts = append(opts, withBaseURL(s.URL))
	c, err := New(testConfig(), s.clk, log, opts...)
	if err != nil {
		t.Fatalf("New returned an unexpected error: %v", err)
	}
	return c, logs
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError answers with the shape GitHub's REST API uses for a failure, so
// go-github's own CheckResponse classifies it exactly as it would in
// production.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"message": message})
}

// decodeBody unmarshals a recorded request body.
func decodeBody(t *testing.T, r recorded) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(r.Body, &got); err != nil {
		t.Fatalf("decoding the %s %s body %q: %v", r.Method, r.Path, r.Body, err)
	}
	return got
}

// slogDiscard is a logger for tests that assert on behaviour rather than logs.
func slogDiscard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
