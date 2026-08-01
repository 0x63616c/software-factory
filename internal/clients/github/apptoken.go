package github

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/golang-jwt/jwt/v5"
	gh "github.com/google/go-github/v78/github"
)

// GitHub's own limits on an App JWT, and how far we stay inside them.
const (
	// jwtLifetime is GitHub's hard maximum for an App JWT.
	jwtLifetime = 10 * time.Minute

	// jwtClockSkew pulls both ends of the JWT's window inward. GitHub validates
	// iat and exp against ITS clock, not ours, and pod clock skew is real: a
	// local clock running fast issues a JWT that GitHub reads as issued in its
	// future or as expiring past its ceiling. Either is a 401 that arrives
	// intermittently and reads as random. GitHub's own documentation backdates
	// iat for exactly this reason; exp earns the same margin.
	jwtClockSkew = 60 * time.Second
)

// appAuth holds the App's identity and the installation token minted from it.
//
// Two credential planes live here and neither leaves the package: the App JWT,
// which proves who the App is, and the installation token, which is what the
// API actually accepts.
type appAuth struct {
	appID          int64
	installationID int64
	key            *rsa.PrivateKey
	clk            clock.Clock
	log            *slog.Logger

	// exchange is the unauthenticated base client. It must NOT carry
	// installationTransport: that transport performs the exchange, so routing
	// the exchange through it recurses and deadlocks on the mutex the refresh
	// already holds.
	exchange *gh.Client

	// refreshSkew is how long before expiry a token counts as spent. GitHub's
	// expires_at is a SERVER timestamp compared against our local clock, so this
	// absorbs skew as well as the refresh boundary — which is why it is five
	// minutes against a one-hour lifetime and not thirty seconds.
	refreshSkew time.Duration

	mu        sync.Mutex
	token     string
	expiresAt time.Time

	identityMu     sync.Mutex
	cachedBotLogin string
	identity       botAttribution
}

// botAttribution is the validated GitHub identity commits made by a Run Worker
// must carry.
type botAttribution struct {
	Login     string
	AccountID int64
}

// mintJWT signs a fresh App JWT. It is never cached: it costs one RSA
// signature, and a cache would be a second expiry to get wrong.
func (a *appAuth) mintJWT() (string, error) {
	now := a.clk.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(a.appID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtClockSkew)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime - jwtClockSkew)),
	})
	signed, err := token.SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("signing the app jwt: %w", err)
	}
	return signed, nil
}

// appClient returns a client authenticated as the App itself, for the two
// endpoints that take a JWT rather than an installation token.
func (a *appAuth) appClient() (*gh.Client, error) {
	signed, err := a.mintJWT()
	if err != nil {
		return nil, err
	}
	return a.exchange.WithAuthToken(signed), nil
}

// currentToken returns a valid installation token for this service's own calls,
// exchanging for a new one when the cached one is spent.
//
// The mutex is held across the network refresh, so a burst of callers costs one
// exchange instead of N. This service is LLM-latency-bound; a few hundred
// milliseconds of contention once an hour is cheaper than a double-checked
// locking bug.
func (a *appAuth) currentToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token != "" && a.clk.Now().Add(a.refreshSkew).Before(a.expiresAt) {
		return a.token, nil
	}

	token, expiresAt, err := a.mint(ctx, "exchanging the app jwt for an installation token", nil)
	if err != nil {
		// Nothing is cached on failure: expiresAt is assigned only below.
		return "", err
	}

	a.token, a.expiresAt = token, expiresAt
	a.log.InfoContext(ctx, "refreshed the github installation token",
		"installation_id", a.installationID, "expires_at", expiresAt.UTC().Format(time.RFC3339))
	return a.token, nil
}

// mint performs one token exchange. opts nil means the installation's full
// scope, which is what this service uses for its own calls.
func (a *appAuth) mint(ctx context.Context, op string, opts *gh.InstallationTokenOptions) (string, time.Time, error) {
	client, err := a.appClient()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s: %w", op, err)
	}

	token, _, err := client.Apps.CreateInstallationToken(ctx, a.installationID, opts)
	if err != nil {
		return "", time.Time{}, classifyExchange(ctx, op, a.installationID, err)
	}
	if token.GetToken() == "" {
		return "", time.Time{}, fmt.Errorf("%s: github returned an empty token", op)
	}
	return token.GetToken(), token.GetExpiresAt().Time, nil
}

// classifyExchange is classify, plus the one status code that means something
// different here.
//
// A 422 from a token exchange is not "we sent something malformed": narrowing
// permissions is a subset operation, so it is GitHub saying the installation
// does not hold a permission we asked to narrow to. Permissions added to an
// App's manifest after installation sit as a PENDING request until the owner
// accepts, so this is a live failure mode — and the generic "malformed request"
// verdict would send whoever reads it looking at our JSON.
func classifyExchange(ctx context.Context, op string, installationID int64, err error) error {
	var resp *gh.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.Response.StatusCode {
		case http.StatusUnprocessableEntity:
			return permanent(op, ErrAuth, fmt.Errorf(
				"the installation has not granted every permission this token requests; approve the pending permission request in the App's settings: %w", err))
		case http.StatusNotFound:
			// This endpoint addresses exactly one resource, our own
			// installation, so "not found" is never about a ticket: the App has
			// been uninstalled from the repository, or the installation id is
			// wrong.
			return permanent(op, ErrAuth, fmt.Errorf(
				"installation %d does not exist or this app can no longer reach it: %w", installationID, err))
		}
	}
	return classify(ctx, op, err)
}

// botLogin resolves the login GitHub attributes this App's comments to, which
// is the App's slug with a "[bot]" suffix.
//
// Resolved lazily and once, because it never changes and because GET /app costs
// a request the common path does not need. It is read through the App JWT
// plane: that endpoint does not accept an installation token.
func (a *appAuth) botLogin(ctx context.Context) (string, error) {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	return a.botLoginLocked(ctx)
}

func (a *appAuth) botLoginLocked(ctx context.Context) (string, error) {
	if a.cachedBotLogin != "" {
		return a.cachedBotLogin, nil
	}

	client, err := a.appClient()
	if err != nil {
		return "", err
	}
	app, _, err := client.Apps.Get(ctx, "")
	if err != nil {
		return "", classify(ctx, "reading this app's own identity", err)
	}
	if app.GetSlug() == "" {
		return "", fmt.Errorf("reading this app's own identity: github returned no slug")
	}

	a.cachedBotLogin = app.GetSlug() + "[bot]"
	return a.cachedBotLogin, nil
}

// attribution resolves the complete identity a Run Worker needs to configure git
// commit attribution. The public user lookup intentionally uses the base
// client: GET /users/{username} accepts no App JWT, and a Run Worker token must
// not be minted before this identity is known to be valid.
func (a *appAuth) attribution(ctx context.Context) (botAttribution, error) {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()

	if a.identity.AccountID != 0 {
		return a.identity, nil
	}

	login, err := a.botLoginLocked(ctx)
	if err != nil {
		return botAttribution{}, err
	}
	user, _, err := a.exchange.Users.Get(ctx, login)
	if err != nil {
		return botAttribution{}, classify(ctx, "reading this app bot's public profile", err)
	}
	if user.GetID() == 0 {
		return botAttribution{}, fmt.Errorf("reading this app bot's public profile: github returned no account id")
	}
	if user.GetLogin() == "" {
		return botAttribution{}, fmt.Errorf("reading this app bot's public profile: github returned no login")
	}
	if user.GetLogin() != login {
		return botAttribution{}, fmt.Errorf("reading this app bot's public profile: github returned login %q, want %q", user.GetLogin(), login)
	}

	a.identity = botAttribution{Login: user.GetLogin(), AccountID: user.GetID()}
	return a.identity, nil
}
