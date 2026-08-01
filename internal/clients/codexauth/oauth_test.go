package codexauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

const (
	testClientID = "test-client-id"
	testToken    = "SECRET-REFRESH-VALUE"
)

func newTestRefresher(t *testing.T, handler http.HandlerFunc) *HTTPRefresher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	refresher, err := NewHTTPRefresher(&http.Client{Timeout: 5 * time.Second}, server.URL, testClientID)
	if err != nil {
		t.Fatalf("NewHTTPRefresher: %v", err)
	}
	return refresher
}

func TestHTTPRefresherPostsTheRefreshGrantAsJSON(t *testing.T) {
	t.Parallel()
	var (
		gotURL         string
		gotContentType string
		gotBody        []byte
	)
	refresher := newTestRefresher(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotURL, gotContentType = r.URL.String(), r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","id_token":"i"}`))
	})

	if _, _, err := refresher.Refresh(context.Background(), work.NewCredential(testToken)); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// JSON, not form encoding. Verified against codex-cli's own refresh at
	// rust-v0.145.0; its authorization_code exchange IS form-encoded, which is
	// the trap this asserts against.
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("the request body is not JSON: %v", err)
	}
	want := map[string]any{"grant_type": "refresh_token", "client_id": testClientID, "refresh_token": testToken}
	for key, value := range want {
		if sent[key] != value {
			t.Errorf("request body %s = %v, want %v", key, sent[key], value)
		}
	}
	if len(sent) != len(want) {
		t.Errorf("the request body carries %d fields, want exactly %v", len(sent), want)
	}
	// A token in a query string is a token in every proxy and access log
	// between here and the provider.
	if strings.Contains(gotURL, testToken) {
		t.Error("the refresh token reached the URL; it belongs in the body and nowhere else")
	}
}

func TestHTTPRefresherParsesTheRotatedPairAsARotationOutcome(t *testing.T) {
	t.Parallel()
	refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-a","refresh_token":"new-r","id_token":"new-i"}`))
	})

	res, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if outcome != RefreshRotated {
		t.Fatalf("outcome = %s, want %s", outcome, RefreshRotated)
	}
	if res.AccessToken.Reveal() != "new-a" || res.RefreshToken.Reveal() != "new-r" || res.IDToken.Reveal() != "new-i" {
		t.Error("Refresh did not return all three rotated tokens")
	}
}

func TestHTTPRefresherLeavesAnOmittedTokenUnchanged(t *testing.T) {
	t.Parallel()
	// Every field of the response is optional, and the CLI keeps whatever it
	// already held for an absent one. An omitted refresh_token means the
	// provider did not rotate it, so the stored one is still live — blanking
	// it would be the dead credential, not keeping it.
	refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-a"}`))
	})

	res, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if outcome != RefreshRotated {
		t.Fatalf("outcome = %s, want %s", outcome, RefreshRotated)
	}
	if res.AccessToken.Reveal() != "new-a" {
		t.Error("the rotated access token was not returned")
	}
	if res.RefreshToken.Reveal() != "" || res.IDToken.Reveal() != "" {
		t.Error("an omitted field must come back empty, meaning unchanged, not invented")
	}
}

func TestHTTPRefresherReturnsARotatedRefreshTokenEvenWithNoAccessToken(t *testing.T) {
	t.Parallel()
	// Every field of the response is optional, access_token included, and the
	// CLI keeps the stored value for each absent one. So a 200 carrying only a
	// rotated refresh token is a successful, non-destructive refresh — and
	// discarding it would spend the old token and drop its replacement, which
	// is the dead credential this package exists to prevent.
	refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"refresh_token":"new-r"}`))
	})

	res, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if outcome != RefreshRotated {
		t.Fatalf("outcome = %s, want %s — the grant was spent and its replacement arrived", outcome, RefreshRotated)
	}
	if res.RefreshToken.Reveal() != "new-r" {
		t.Fatal("the rotated refresh token was discarded; the old one is spent and nothing stores its replacement")
	}
}

func TestHTTPRefresherReportsAReusedRefreshTokenDistinctlyFromARefusal(t *testing.T) {
	t.Parallel()
	// "Reused" is the provider telling us something else already presented
	// this token. That is INV-1 violated outside this process, and its
	// recovery is to find the other holder — not to re-seed, which would just
	// feed the second holder a fresh credential to eat.
	refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"refresh_token_reused"}}`))
	})

	_, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if outcome != RefreshReused {
		t.Fatalf("outcome = %s, want %s", outcome, RefreshReused)
	}
	if err == nil {
		t.Fatal("Refresh succeeded on a reused token")
	}
}

func TestHTTPRefresherMatchesRefusalCodesRegardlessOfCase(t *testing.T) {
	t.Parallel()
	// The CLI lowercases before matching. Mislabelling a refusal as unknown
	// routes the operator to the runbook row that offers one more presentation
	// of a possibly-spent token.
	for _, body := range []string{`{"error":"Refresh_Token_Reused"}`, `{"error":{"code":"REFRESH_TOKEN_REUSED"}}`} {
		refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
		})
		if _, outcome, _ := refresher.Refresh(context.Background(), work.NewCredential(testToken)); outcome != RefreshReused {
			t.Errorf("%s gave outcome %s, want %s", body, outcome, RefreshReused)
		}
	}
}

func TestHTTPRefresherRefusesToFollowARedirect(t *testing.T) {
	t.Parallel()
	// Go replays a POST body on 307/308, GetBody and all, so a redirect would
	// hand the refresh token to another host — including a plaintext one,
	// defeating the https check the constructor performs on the initial URL.
	var landed atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), testToken) {
			landed.Store(true)
		}
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r"}`))
	}))
	t.Cleanup(elsewhere.Close)

	refresher := newTestRefresher(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	})

	_, _, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if landed.Load() {
		t.Fatal("the refresh token was replayed to a second host by a redirect")
	}
	if err == nil {
		t.Error("Refresh followed a redirect instead of refusing it")
	}
}

func TestHTTPRefresherReportsARefusedTokenAsARejection(t *testing.T) {
	t.Parallel()
	// The codes the provider actually uses, and the three shapes it puts them
	// in. Read from codex-cli rust-v0.145.0 rather than assumed from RFC 6749
	// — invalid_grant alone would miss every one of the reuse cases.
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"a bare error string":         {400, `{"error":"refresh_token_expired"}`, "refresh_token_expired"},
		"an error object with a code": {400, `{"error":{"code":"refresh_token_invalidated"}}`, "refresh_token_invalidated"},
		"a top-level code":            {400, `{"code":"refresh_token_invalidated"}`, "refresh_token_invalidated"},
		"a standard invalid grant":    {400, `{"error":"invalid_grant"}`, "invalid_grant"},
		// 401 is permanent whatever it says: the credential is not accepted.
		"an unauthorized response with no code": {401, `{}`, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			_, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
			if outcome != RefreshRejected {
				t.Fatalf("outcome = %s, want %s", outcome, RefreshRejected)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("error = %v, want it to name %q", err, c.want)
			}
		})
	}
}

func TestHTTPRefresherReportsAnUnrecognisedRefusalAsUnknown(t *testing.T) {
	t.Parallel()
	// A 400 the provider did not explain is not evidence the grant survived.
	refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"something_new"}`))
	})
	_, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if outcome != RefreshUnknown {
		t.Fatalf("outcome = %s, want %s", outcome, RefreshUnknown)
	}
	if err == nil {
		t.Fatal("Refresh succeeded on a 400")
	}
}

func TestHTTPRefresherReportsAServerFailureAsAnUnknownOutcome(t *testing.T) {
	t.Parallel()
	// The deliberate availability sacrifice. A token endpoint that answered
	// non-200 may or may not have consumed the grant, and for a single-use
	// credential an unknown is an unknown.
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"server_error"}`))
		})
		_, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
		if outcome != RefreshUnknown {
			t.Errorf("status %d gave outcome %s, want %s — treating it as retryable presents the token twice", status, outcome, RefreshUnknown)
		}
		if err == nil {
			t.Errorf("status %d gave no error", status)
		}
	}
}

func TestHTTPRefresherReportsAnUnreachableEndpointAsNeverSent(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	refresher, err := NewHTTPRefresher(&http.Client{Timeout: 5 * time.Second}, url, testClientID)
	if err != nil {
		t.Fatalf("NewHTTPRefresher: %v", err)
	}
	_, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if err == nil {
		t.Fatal("Refresh succeeded against a closed listener")
	}
	// Nothing was written, so the token is untouched and this is an ordinary
	// blip. This is the classification that keeps INV-2 affordable.
	if outcome != RefreshNotSent {
		t.Errorf("outcome = %s, want %s", outcome, RefreshNotSent)
	}
}

func TestHTTPRefresherReportsAHungResponseAsAnUnknownOutcome(t *testing.T) {
	t.Parallel()
	hang := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	t.Cleanup(func() { close(hang); server.Close() })

	refresher, err := NewHTTPRefresher(&http.Client{Timeout: 50 * time.Millisecond}, server.URL, testClientID)
	if err != nil {
		t.Fatalf("NewHTTPRefresher: %v", err)
	}
	_, outcome, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if err == nil {
		t.Fatal("Refresh succeeded against a handler that never answered")
	}
	// The request was written. The provider may well have rotated the pair and
	// we simply did not hear it, so this must never be retried.
	if outcome != RefreshUnknown {
		t.Errorf("outcome = %s, want %s — the request reached the wire", outcome, RefreshUnknown)
	}
}

func TestNewHTTPRefresherRefusesAnUnusableConfiguration(t *testing.T) {
	t.Parallel()
	cases := map[string]func() (*HTTPRefresher, error){
		// An unbounded presentation would invalidate the lease-expiry
		// reasoning the takeover policy rests on.
		"a client with no timeout": func() (*HTTPRefresher, error) {
			return NewHTTPRefresher(&http.Client{}, "https://example.invalid/token", testClientID)
		},
		"no client": func() (*HTTPRefresher, error) {
			return NewHTTPRefresher(nil, "https://example.invalid/token", testClientID)
		},
		"no token URL": func() (*HTTPRefresher, error) {
			return NewHTTPRefresher(&http.Client{Timeout: time.Second}, "", testClientID)
		},
		"no client ID": func() (*HTTPRefresher, error) {
			return NewHTTPRefresher(&http.Client{Timeout: time.Second}, "https://example.invalid/token", "")
		},
		// The token travels in the request body, so plaintext to anywhere
		// but loopback puts it on the wire in the clear.
		"a plaintext URL to a remote host": func() (*HTTPRefresher, error) {
			return NewHTTPRefresher(&http.Client{Timeout: time.Second}, "http://example.invalid/token", testClientID)
		},
		"a URL that is not a URL": func() (*HTTPRefresher, error) {
			return NewHTTPRefresher(&http.Client{Timeout: time.Second}, "://nope", testClientID)
		},
	}
	for name, construct := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refresher, err := construct()
			if err == nil {
				t.Fatal("NewHTTPRefresher returned a usable-but-invalid refresher, want an error")
			}
			if refresher != nil {
				t.Error("NewHTTPRefresher returned both a refresher and an error")
			}
		})
	}
}

func TestHTTPRefresherNeverPutsATokenInAnError(t *testing.T) {
	t.Parallel()
	// Errors from here are wrapped and logged all the way up. One that
	// interpolated the token would write it to Loki on every failure.
	refresher := newTestRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	res, _, err := refresher.Refresh(context.Background(), work.NewCredential(testToken))
	if err == nil {
		t.Fatal("Refresh succeeded")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("the refresh token reached an error string")
	}
	if strings.Contains(res.String(), testToken) {
		t.Error("the refresh token reached a rendered Refreshed")
	}
}
