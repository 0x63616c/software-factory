package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	factoryapi "github.com/0x63616c/software-factory/internal/api"
)

const (
	testIssuer   = "https://test.cloudflareaccess.com"
	testAudience = "factory-api-audience"
)

var (
	futureExpiry = time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	pastExpiry   = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
)

func TestMiddlewareAuthenticatesAccessAndBearerIdentities(t *testing.T) {
	key := newRSAKey(t)
	server := newJWKSServer(t, testJWKS(t, "first", key))
	middleware := newMiddleware(t, server.URL, "worker-token", "sandbox-token")

	for _, test := range []struct {
		name     string
		header   string
		value    string
		identity Identity
	}{
		{"access", accessHeader, signedToken(t, key, "first", testAudience, futureExpiry), Identity{Kind: IdentityAccess, Scope: ScopeWrite}},
		{"worker", "Authorization", "Bearer worker-token", Identity{Kind: IdentityWorker, Scope: ScopeWrite}},
		{"run worker", "Authorization", "Bearer sandbox-token", Identity{Kind: IdentityRunWorker, Scope: ScopeRead}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/build", nil)
			request.Header.Set(test.header, test.value)
			response := httptest.NewRecorder()
			middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if got := IdentityFromContext(request.Context()); got != test.identity {
					t.Fatalf("identity = %#v, want %#v", got, test.identity)
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
			}
		})
	}
}

func TestMiddlewareRefusesRunWorkerBearerForEveryFactoryCommand(t *testing.T) {
	key := newRSAKey(t)
	server := newJWKSServer(t, testJWKS(t, "first", key))
	middleware := newMiddleware(t, server.URL, "worker-token", "sandbox-token")
	handler := middleware.Wrap(factoryapi.New("test-build", nil).Handler())

	for _, requestTarget := range []struct{ method, path string }{
		{http.MethodPost, "/v1/factory/pause"},
		{http.MethodPost, "/v1/factory/resume"},
		{http.MethodPost, "/v1/factory/max-in-flight"},
		{http.MethodPost, "/v1/tickets/42/cancel"},
		{http.MethodPost, "/v1/tickets/42/work"},
		{http.MethodPost, "/v1/tickets"},
		{http.MethodPatch, "/v1/tickets/42/state"},
		{http.MethodPut, "/v1/tickets/42/blockers/43"},
		{http.MethodDelete, "/v1/tickets/42/blockers/43"},
	} {
		t.Run(requestTarget.method+" "+requestTarget.path, func(t *testing.T) {
			request := httptest.NewRequest(requestTarget.method, requestTarget.path, nil)
			request.Header.Set("Authorization", "Bearer sandbox-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s status = %d, want %d", requestTarget.method, requestTarget.path, response.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestMiddlewareUniformlyRejectsInvalidCredentialsAndWriteScope(t *testing.T) {
	key := newRSAKey(t)
	otherKey := newRSAKey(t)
	server := newJWKSServer(t, testJWKS(t, "first", key))
	middleware := newMiddleware(t, server.URL, "worker-token", "sandbox-token")

	for _, test := range []struct {
		name   string
		method string
		header string
		value  string
	}{
		{"missing", http.MethodGet, "", ""},
		{"cookie only", http.MethodGet, "Cookie", "CF_Authorization=not-used"},
		{"wrong bearer", http.MethodGet, "Authorization", "Bearer incorrect-token"},
		{"malformed bearer", http.MethodGet, "Authorization", "Basic sandbox-token"},
		{"bad signature", http.MethodGet, accessHeader, signedToken(t, otherKey, "first", testAudience, futureExpiry)},
		{"expired", http.MethodGet, accessHeader, signedToken(t, key, "first", testAudience, pastExpiry)},
		{"wrong audience", http.MethodGet, accessHeader, signedToken(t, key, "first", "another-application", futureExpiry)},
		{"read scope mutation", http.MethodPost, "Authorization", "Bearer sandbox-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/v1/build", nil)
			if test.header != "" {
				request.Header.Set(test.header, test.value)
			}
			response := httptest.NewRecorder()
			middleware.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("rejected request reached handler")
			})).ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || response.Body.String() != rejectionBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", response.Code, response.Body.String(), http.StatusUnauthorized, rejectionBody)
			}
		})
	}
}

func TestMiddlewareRefreshesJWKSForUnknownKeyID(t *testing.T) {
	first := newRSAKey(t)
	second := newRSAKey(t)
	keys := testJWKS(t, "first", first)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = writer.Write(keys)
	}))
	t.Cleanup(server.Close)
	middleware := newMiddleware(t, server.URL, "worker-token", "sandbox-token")
	if requests != 1 {
		t.Fatalf("initial JWKS requests = %d, want 1", requests)
	}
	keys = testJWKS(t, "second", second)
	request := httptest.NewRequest(http.MethodGet, "/v1/build", nil)
	request.Header.Set(accessHeader, signedToken(t, second, "second", testAudience, futureExpiry))
	response := httptest.NewRecorder()
	middleware.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || requests < 2 {
		t.Fatalf("response status = %d and JWKS requests = %d, want accepted after refresh", response.Code, requests)
	}
}

func newMiddleware(t *testing.T, certsURL, workerBearer, runWorkerBearer string) *Middleware {
	t.Helper()
	middleware, err := New(Options{
		AccessIssuer:    testIssuer,
		AccessAudience:  testAudience,
		AccessCertsURL:  certsURL,
		WorkerBearer:    workerBearer,
		RunWorkerBearer: runWorkerBearer,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return middleware
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

func testJWKS(t *testing.T, keyID string, key *rsa.PrivateKey) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": keyID, "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}
	return payload
}

func newJWKSServer(t *testing.T, keys []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(keys)
	}))
	t.Cleanup(server.Close)
	return server
}

func signedToken(t *testing.T, key *rsa.PrivateKey, keyID, audience string, expiresAt time.Time) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer: testIssuer, Audience: jwt.ClaimStrings{audience}, ExpiresAt: jwt.NewNumericDate(expiresAt),
	})
	token.Header["kid"] = keyID
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return signed
}
