// Package auth parses API credentials into scoped caller identities.
package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

const (
	accessHeader  = "Cf-Access-Jwt-Assertion"
	rejectionBody = "unauthorized\n"
)

type contextKey struct{}

// Scope controls which HTTP methods an authenticated caller can use.
type Scope string

const (
	// ScopeRead allows safe HTTP methods only.
	ScopeRead Scope = "read"
	// ScopeWrite allows every HTTP method.
	ScopeWrite Scope = "write"
)

// IdentityKind identifies the credential source behind an Identity.
type IdentityKind string

const (
	// IdentityAccess is a caller authenticated by Cloudflare Access.
	IdentityAccess IdentityKind = "access"
	// IdentityWorker is the factory worker's in-cluster bearer.
	IdentityWorker IdentityKind = "worker"
	// IdentityRunWorker is a Run Worker's read-only in-cluster bearer.
	IdentityRunWorker IdentityKind = "run_worker"
)

// Identity is the typed caller value available to API handlers.
type Identity struct {
	Kind  IdentityKind
	Scope Scope
}

// Options configures a Middleware instance.
type Options struct {
	AccessIssuer    string
	AccessAudience  string
	AccessCertsURL  string
	WorkerBearer    string
	RunWorkerBearer string
	HTTPClient      *http.Client
}

// Middleware authenticates API callers and enforces their scope before handlers run.
type Middleware struct {
	accessVerifier  keyfunc.Keyfunc
	accessIssuer    string
	accessAudience  string
	workerBearer    []byte
	runWorkerBearer []byte
}

// New constructs authentication middleware and loads the current Cloudflare JWKS.
func New(options Options) (*Middleware, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}

	storage, err := jwkset.NewStorageFromHTTP(options.AccessCertsURL, jwkset.HTTPClientStorageOptions{
		Client:          options.HTTPClient,
		Ctx:             context.Background(),
		HTTPTimeout:     10 * time.Second,
		RefreshInterval: time.Hour,
	})
	if err != nil {
		return nil, fmt.Errorf("load Cloudflare Access JWKS: %w", err)
	}
	keySet, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{options.AccessCertsURL: storage},
		PrioritizeHTTP:    true,
		RefreshUnknownKID: rate.NewLimiter(rate.Every(time.Second), 1),
	})
	if err != nil {
		return nil, fmt.Errorf("configure Cloudflare Access JWKS cache: %w", err)
	}
	verifier, err := keyfunc.New(keyfunc.Options{Ctx: context.Background(), Storage: keySet})
	if err != nil {
		return nil, fmt.Errorf("create Cloudflare Access JWT verifier: %w", err)
	}
	return &Middleware{
		accessVerifier:  verifier,
		accessIssuer:    options.AccessIssuer,
		accessAudience:  options.AccessAudience,
		workerBearer:    []byte(options.WorkerBearer),
		runWorkerBearer: []byte(options.RunWorkerBearer),
	}, nil
}

func (options Options) validate() error {
	for name, value := range map[string]string{
		"access issuer": options.AccessIssuer, "access audience": options.AccessAudience,
		"access certificates URL": options.AccessCertsURL, "worker bearer": options.WorkerBearer,
		"run worker bearer": options.RunWorkerBearer,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

// Wrap authenticates every request and centrally rejects write attempts by read-only callers.
func (middleware *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identity, ok := middleware.authenticate(request)
		if !ok || identity.Scope == ScopeRead && !isReadMethod(request.Method) {
			reject(writer)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), contextKey{}, identity)))
	})
}

func (middleware *Middleware) authenticate(request *http.Request) (Identity, bool) {
	if assertion := request.Header.Get(accessHeader); assertion != "" {
		return middleware.accessIdentity(request.Context(), assertion)
	}
	bearer, ok := parseBearer(request.Header.Get("Authorization"))
	if !ok {
		return Identity{}, false
	}
	if subtle.ConstantTimeCompare([]byte(bearer), middleware.workerBearer) == 1 {
		return Identity{Kind: IdentityWorker, Scope: ScopeWrite}, true
	}
	if subtle.ConstantTimeCompare([]byte(bearer), middleware.runWorkerBearer) == 1 {
		return Identity{Kind: IdentityRunWorker, Scope: ScopeRead}, true
	}
	return Identity{}, false
}

func (middleware *Middleware) accessIdentity(ctx context.Context, assertion string) (Identity, bool) {
	parser := jwt.NewParser(
		jwt.WithAudience(middleware.accessAudience),
		jwt.WithIssuer(middleware.accessIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
	)
	if _, err := parser.Parse(assertion, middleware.accessVerifier.KeyfuncCtx(ctx)); err != nil {
		return Identity{}, false
	}
	return Identity{Kind: IdentityAccess, Scope: ScopeWrite}, true
}

func parseBearer(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.TrimSpace(strings.TrimPrefix(header, prefix)) == "" {
		return "", false
	}
	return strings.TrimPrefix(header, prefix), true
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func reject(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusUnauthorized)
	_, _ = writer.Write([]byte(rejectionBody))
}

// IdentityFromContext returns the identity parsed by Middleware.Wrap.
func IdentityFromContext(ctx context.Context) Identity {
	identity, _ := ctx.Value(contextKey{}).(Identity)
	return identity
}
