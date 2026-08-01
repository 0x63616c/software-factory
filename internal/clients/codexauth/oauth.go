package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/0x63616c/software-factory/internal/work"
)

// The provider's own OAuth facts, read from codex-cli rust-v0.145.0 rather
// than from memory: REFRESH_TOKEN_URL and CLIENT_ID in
// codex-rs/login/src/auth/manager.rs. They live here so the fact has one home,
// and they are still constructor arguments so a test never needs them.
//
// DefaultClientID is a public OAuth client id, not a credential: it is a
// literal in an open-source CLI and identifies the application, not its holder.
const (
	DefaultTokenURL = "https://auth.openai.com/oauth/token"
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// maxResponseBytes bounds what is read back. The response is three short
// strings; anything larger is a misconfigured endpoint, not a credential.
const maxResponseBytes = 1 << 20

// HTTPRefresher is the real TokenRefresher, talking to the provider's token
// endpoint.
//
// The endpoint and the client ID are constructor arguments rather than
// constants because they are the provider's facts, not this package's, and the
// composition root is where facts about the outside world are chosen.
type HTTPRefresher struct {
	client   *http.Client
	tokenURL string
	clientID string
}

// NewHTTPRefresher constructs one.
//
// It rejects a client with no Timeout. An unbounded presentation would
// invalidate the lease-expiry reasoning this package's takeover policy depends
// on, and a single mechanism failing silently would leave that reasoning
// looking sound while being false.
func NewHTTPRefresher(client *http.Client, tokenURL, clientID string) (*HTTPRefresher, error) {
	switch {
	case client == nil:
		return nil, fmt.Errorf("a codex token refresher needs an HTTP client")
	case client.Timeout <= 0:
		return nil, fmt.Errorf("a codex token refresher needs a client with a timeout: an unbounded presentation makes an expired lease uninterpretable")
	case tokenURL == "":
		return nil, fmt.Errorf("a codex token refresher needs the provider's token endpoint")
	case clientID == "":
		return nil, fmt.Errorf("a codex token refresher needs the OAuth client id")
	}
	parsed, err := url.Parse(tokenURL)
	if err != nil {
		return nil, fmt.Errorf("the codex token endpoint %q is not a URL: %w", tokenURL, err)
	}
	// The refresh token travels in the request body, so plaintext to anywhere
	// but loopback puts a credential on the wire in the clear.
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopback(parsed.Host)) {
		return nil, fmt.Errorf("the codex token endpoint %q must be https, or http to loopback for tests", tokenURL)
	}
	// Go replays a POST body on 307/308 — NewRequestWithContext sets GetBody
	// for a *bytes.Reader — so a redirect would hand the refresh token to
	// whatever host the response names, including a plaintext one. That would
	// defeat the scheme check above, which only ever sees the initial URL.
	//
	// The client is copied rather than mutated: the caller may share it, and
	// silently changing its redirect policy would be a side effect nobody
	// asked for.
	bounded := *client
	bounded.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return fmt.Errorf("refusing to follow a redirect to %s: the refresh token travels in the request body and must reach only the configured endpoint", req.URL.Host)
	}
	return &HTTPRefresher{client: &bounded, tokenURL: tokenURL, clientID: clientID}, nil
}

func isLoopback(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// Refresh exchanges a refresh token for a new pair, and says what became of the
// token it was given.
func (r *HTTPRefresher) Refresh(ctx context.Context, refreshToken work.Credential) (Refreshed, RefreshOutcome, error) {
	// JSON, not form encoding. Verified against codex-cli rust-v0.145.0: its
	// refresh posts application/json, while its authorization_code exchange is
	// form-encoded — reading the wrong one of the two is the available trap.
	body, err := json.Marshal(refreshRequest{
		ClientID:     r.clientID,
		GrantType:    "refresh_token",
		RefreshToken: refreshToken.Reveal(),
	})
	if err != nil {
		return Refreshed{}, RefreshNotSent, fmt.Errorf("encoding the codex refresh request: %w", err)
	}

	// "Did the request reach the wire" is decided by the transport telling us,
	// not by matching error strings. Conservative in one direction on purpose:
	// WroteRequest counts even when it reports an error, because a partially
	// written request wrongly called "not sent" licenses a second presentation,
	// while a failed request wrongly called "sent" only costs a needless halt.
	// The error directions are asymmetric and so is the rule.
	var reached atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { reached.Store(true) },
		GotFirstResponseByte: func() { reached.Store(true) },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, r.tokenURL, bytes.NewReader(body))
	if err != nil {
		return Refreshed{}, RefreshNotSent, fmt.Errorf("building the codex refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		if reached.Load() {
			return Refreshed{}, RefreshUnknown, fmt.Errorf("the codex refresh request was sent and no usable answer came back: %w", err)
		}
		return Refreshed{}, RefreshNotSent, fmt.Errorf("the codex refresh request never reached %s: %w", r.tokenURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Refreshed{}, RefreshUnknown, fmt.Errorf("reading the codex token endpoint's answer: %w", err)
	}
	return classify(resp.StatusCode, answer)
}

// refreshRequest is the refresh grant, in the shape the provider expects.
type refreshRequest struct {
	ClientID     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
}

// tokenResponse is the token endpoint's answer.
//
// All three tokens are optional, and an absent one means "unchanged" rather
// than "blank" — the CLI keeps whatever it already held. Error arrives as
// either a bare string or an object carrying a code, and a code can also sit
// at the top level, so all three shapes are modelled rather than guessed at.
type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	IDToken      string          `json:"id_token"`
	Error        json.RawMessage `json:"error"`
	Description  string          `json:"error_description"`
	Code         string          `json:"code"`
}

// errorCode reads the provider's machine-readable reason out of whichever of
// the three shapes it used.
func (r tokenResponse) errorCode() string {
	var bare string
	if err := json.Unmarshal(r.Error, &bare); err == nil && bare != "" {
		return strings.ToLower(bare)
	}
	var object struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(r.Error, &object); err == nil && object.Code != "" {
		return strings.ToLower(object.Code)
	}
	return strings.ToLower(r.Code)
}

// refusals are the codes that mean the refresh token itself is finished.
//
// The first three are the CLI's own permanent-failure set. invalid_grant is
// NOT — the CLI maps it to a transient error and retries. That divergence is
// deliberate and must not be "corrected" back: retrying a single-use grant is
// how a live credential becomes a dead one, and this package's whole asymmetry
// says an unrecoverable mistake beats an unnecessary halt.
//
// Matched lowercased, as the CLI does, so a provider that changes the case of
// a code cannot silently demote a refusal to an unknown outcome — which would
// route an operator to the runbook row offering one more presentation.
var refusals = map[string]bool{
	"refresh_token_expired":     true,
	"refresh_token_reused":      true,
	"refresh_token_invalidated": true,
	"invalid_grant":             true,
}

// reusedCode is the one refusal with a different recovery: something else
// already presented this token.
const reusedCode = "refresh_token_reused"

// classify turns one HTTP answer into an outcome.
//
// Everything that is not an unambiguous success or an unambiguous refusal is
// unknown — including 429 and every 5xx. That is a deliberate availability
// sacrifice: an endpoint that answered non-200 may or may not have consumed the
// grant, and for a single-use credential, guessing "not consumed" is the guess
// that destroys it.
func classify(status int, body []byte) (Refreshed, RefreshOutcome, error) {
	var parsed tokenResponse
	// A body that does not parse is not fatal to the classification; the
	// status still carries most of the answer.
	parseErr := json.Unmarshal(body, &parsed)

	switch {
	case status == http.StatusOK:
		if parseErr != nil {
			return Refreshed{}, RefreshUnknown, fmt.Errorf("the codex token endpoint answered 200 with a body that is not JSON: %w", parseErr)
		}
		// Every field is optional, access_token included, and an absent one
		// means unchanged. So a 200 is a successful rotation whatever subset
		// it carries, and everything it did return must be handed back to be
		// stored — the grant is spent either way, and dropping its replacement
		// is what kills the credential. Whether what came back is usable is
		// the caller's decision, made after it is durable.
		return Refreshed{
			AccessToken:  work.NewCredential(parsed.AccessToken),
			RefreshToken: work.NewCredential(parsed.RefreshToken),
			IDToken:      work.NewCredential(parsed.IDToken),
		}, RefreshRotated, nil

	case parsed.errorCode() == reusedCode:
		return Refreshed{}, RefreshReused, fmt.Errorf("the codex token endpoint reports this refresh token was already presented (%d %s)", status, parsed.errorCode())

	// 401 is a refusal whatever it says: the credential was not accepted.
	case status == http.StatusUnauthorized || refusals[parsed.errorCode()]:
		// The provider's error_description is free text under its control and
		// is deliberately not interpolated — an error string here reaches the
		// cluster's log pipeline, and the code alone identifies the condition.
		return Refreshed{}, RefreshRejected, fmt.Errorf("the codex token endpoint refused the refresh token (%d %s)", status, parsed.errorCode())

	default:
		return Refreshed{}, RefreshUnknown, fmt.Errorf("the codex token endpoint answered %d (%s), so whether it consumed the refresh token is unknown", status, parsed.errorCode())
	}
}
