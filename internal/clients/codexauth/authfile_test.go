package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

// unsignedJWT builds a token with the given expiry and a signature that is not
// one. Nothing in this package verifies a signature — the provider does that on
// use — so a synthetic token exercises every path a real one would, and no
// credential-shaped string ever enters the repository.
func unsignedJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": exp.Unix()})
	if err != nil {
		t.Fatalf("building a test token: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + "." + enc([]byte("not-a-signature"))
}

// seedFile builds a stored credential file carrying the given tokens.
func seedFile(t *testing.T, access, refresh string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		keyTokens: map[string]any{
			keyAccessToken:  access,
			keyRefreshToken: refresh,
			keyIDToken:      "stored-id-token",
			keyAccountID:    "acct_stored",
		},
		keyLastRefresh: "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("building a test credential file: %v", err)
	}
	return raw
}

func TestExpiryOfReadsTheExpiryFromAnAccessTokensExpClaim(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	got, err := expiryOf(work.NewCredential(unsignedJWT(t, want)))
	if err != nil {
		t.Fatalf("expiryOf: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("expiry = %s, want %s", got, want)
	}
}

func TestExpiryOfRefusesATokenItCannotRead(t *testing.T) {
	t.Parallel()
	enc := base64.RawURLEncoding.EncodeToString

	// Every one of these must be an error rather than a zero time. "Assume
	// expired" would refresh, fail to read the new token in exactly the same
	// way, and burn the whole credential chain in a loop.
	cases := map[string]string{
		"not three dot-separated segments": "header.payload",
		"a payload that is not base64url":  "aaa.!!!not-base64!!!.ccc",
		"a payload that is not JSON":       "aaa." + enc([]byte("{{{")) + ".ccc",
		"a payload carrying no exp claim":  "aaa." + enc([]byte(`{"sub":"x"}`)) + ".ccc",
		"a non-numeric exp claim":          "aaa." + enc([]byte(`{"exp":"soon"}`)) + ".ccc",
		"an exp claim at or before zero":   "aaa." + enc([]byte(`{"exp":0}`)) + ".ccc",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := expiryOf(work.NewCredential(token)); err == nil {
				t.Fatal("expiryOf accepted a token it cannot read, want an error")
			}
		})
	}
}

func TestCredentialFilePreservesEveryFieldItDoesNotOwnAcrossARotation(t *testing.T) {
	t.Parallel()
	stored := []byte(`{
		"OPENAI_API_KEY": null,
		"some_future_key": {"nested": ["a", 1, true]},
		"tokens": {
			"access_token": "old-access",
			"refresh_token": "old-refresh",
			"id_token": "old-id",
			"account_id": "acct_123",
			"some_future_token_field": 42
		},
		"last_refresh": "2026-07-01T00:00:00Z"
	}`)

	file, err := parseCredentialFile(stored)
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	_, rotated, err := file.withRotation(Refreshed{
		AccessToken:  work.NewCredential("new-access"),
		RefreshToken: work.NewCredential("new-refresh"),
		IDToken:      work.NewCredential("new-id"),
	}, time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("withRotation: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(rotated, &got); err != nil {
		t.Fatalf("the rotated file is not JSON: %v", err)
	}
	tokens, ok := got[keyTokens].(map[string]any)
	if !ok {
		t.Fatalf("tokens = %#v, want an object", got[keyTokens])
	}

	// An unmodelled field is not ours to drop. The next codex release can add
	// one, and a rotation that silently deleted it would corrupt the file for
	// whoever does own it.
	if _, ok := got["OPENAI_API_KEY"]; !ok {
		t.Error("OPENAI_API_KEY was dropped by a rotation")
	}
	if fmt.Sprint(got["some_future_key"]) != `map[nested:[a 1 true]]` {
		t.Errorf("some_future_key = %#v, want it preserved", got["some_future_key"])
	}
	if tokens[keyAccountID] != "acct_123" {
		t.Errorf("tokens.account_id = %#v, want it preserved", tokens[keyAccountID])
	}
	if tokens["some_future_token_field"] != float64(42) {
		t.Errorf("tokens.some_future_token_field = %#v, want it preserved", tokens["some_future_token_field"])
	}
}

// THE invariant of the sandbox file, verified against codex-cli rust-v0.145.0.
// tokens.refresh_token is a bare String on TokenData (token_data.rs:22) — no
// Option, no serde(default), no custom deserializer. So an empty string PARSES,
// while an absent key and a null both FAIL, and they fail the whole document
// rather than that field. "Blank the refresh token" reads naturally as either
// blanking or removing; one of the two produces a sandbox that cannot start.
//
// This is asserted on the decoded JSON rather than through a Go struct so that
// the three cases stay distinguishable: a struct round-trip cannot tell absent
// from blank, which is exactly the distinction that matters here.
func TestSandboxFileBlanksTheRefreshTokenAndNeverRemovesIt(t *testing.T) {
	t.Parallel()

	file, err := parseCredentialFile(seedFile(t, "an-access-token", "the-refresh-token"))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	sandbox, err := file.accessOnly()
	if err != nil {
		t.Fatalf("accessOnly: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(sandbox.Reveal(), &got); err != nil {
		t.Fatalf("the sandbox file is not JSON: %v", err)
	}
	var tokens map[string]json.RawMessage
	if err := json.Unmarshal(got[keyTokens], &tokens); err != nil {
		t.Fatalf("the sandbox file's tokens are not an object: %v", err)
	}

	raw, present := tokens[keyRefreshToken]
	if !present {
		t.Fatalf("tokens.%s is ABSENT from the sandbox file; codex-cli requires the key present (it is a bare String), so the whole file fails to parse and the sandbox never starts", keyRefreshToken)
	}
	if string(raw) == "null" {
		t.Fatalf("tokens.%s is null in the sandbox file; a String cannot deserialize from null, so the whole file fails to parse", keyRefreshToken)
	}
	if string(raw) != `""` {
		t.Errorf("tokens.%s = %s, want the empty string", keyRefreshToken, raw)
	}
}

// Asserting refresh_token == "" only checks the field we thought of. What
// matters is that the token's bytes appear NOWHERE in the document — which
// catches it surfacing in any field, modelled or not, including one a future
// codex release adds.
func TestSandboxFileCarriesTheRefreshTokensBytesNowhereAtAll(t *testing.T) {
	t.Parallel()
	const distinctive = "zzz-refresh-token-that-appears-nowhere-else-zzz"

	file, err := parseCredentialFile(seedFile(t, "an-access-token", distinctive))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	sandbox, err := file.accessOnly()
	if err != nil {
		t.Fatalf("accessOnly: %v", err)
	}

	if strings.Contains(string(sandbox.Reveal()), distinctive) {
		t.Error("the refresh token's bytes appear in the file handed to a sandbox")
	}
}

// accessOnly derives; it must not MUTATE what it derives from. The defensive
// copy of f.tokens is the only thing standing between "derive a sandbox copy"
// and "blank the worker's own live refresh token", and today its absence would
// be harmless purely by call ordering — on both paths accessOnly runs after the
// store.Put. That makes the safety a property of two call sites rather than of
// this method, and one reordering removes it. Asserting non-mutation here makes
// the ordering irrelevant: with the map aliased instead of copied, the STORED
// document gets refresh_token: "", which parseCredentialFile then rejects as
// ErrUnseeded — a bricked credential that only a human running `codex login`
// can fix.
func TestSandboxFileLeavesTheDocumentItDerivedFromUntouched(t *testing.T) {
	t.Parallel()
	const distinctive = "zzz-live-refresh-token-the-worker-still-needs-zzz"

	file, err := parseCredentialFile(seedFile(t, "an-access-token", distinctive))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	if _, err := file.accessOnly(); err != nil {
		t.Fatalf("accessOnly: %v", err)
	}

	if got := file.refresh.Reveal(); got != distinctive {
		t.Errorf("file.refresh = %q after deriving the sandbox copy, want the live token untouched", got)
	}
	// The document as it would be written back to the store. This is the
	// assertion that matters: it is the copy of the token the worker refreshes
	// with next time.
	stored, err := file.encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(stored), distinctive) {
		t.Fatal("deriving the sandbox copy blanked the refresh token in the file it derived from; storing this document would brick the credential")
	}
}

// The sandbox file is DERIVED from the stored one, not composed from parts. A
// composed file is correct only while somebody's list of required fields stays
// complete and current — and those fields' serde attributes are not uniform
// (id_token mandatory and JWT-parsed, OPENAI_API_KEY present-but-nullable,
// refresh_token present-but-blankable). Derived, every key survives because
// nothing enumerated them, so the next codex release adding a mandatory field
// breaks nothing.
func TestSandboxFileChangesNothingButTheRefreshToken(t *testing.T) {
	t.Parallel()
	stored := []byte(`{
		"OPENAI_API_KEY": null,
		"auth_mode": "chatgpt",
		"some_future_key": {"nested": ["a", 1, true]},
		"tokens": {
			"access_token": "the-access-token",
			"refresh_token": "the-refresh-token",
			"id_token": "the-id-token",
			"account_id": "acct_123",
			"some_future_token_field": 42
		},
		"last_refresh": "2026-07-01T00:00:00Z"
	}`)

	file, err := parseCredentialFile(stored)
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	sandbox, err := file.accessOnly()
	if err != nil {
		t.Fatalf("accessOnly: %v", err)
	}

	var want, got map[string]any
	if err := json.Unmarshal(stored, &want); err != nil {
		t.Fatalf("the stored file is not JSON: %v", err)
	}
	if err := json.Unmarshal(sandbox.Reveal(), &got); err != nil {
		t.Fatalf("the sandbox file is not JSON: %v", err)
	}
	// Blank the one field that is meant to differ, then the two documents must
	// be indistinguishable. Comparing whole documents is the point: it fails on
	// a key this test never names.
	want[keyTokens].(map[string]any)[keyRefreshToken] = ""

	if !reflect.DeepEqual(want, got) {
		t.Errorf("the sandbox file differs from the stored one by more than the refresh token:\n got %#v\nwant %#v", got, want)
	}
}

// last_refresh is carried verbatim. codex reads it only as a FALLBACK: it
// returns inside the access-token branch whenever tokens parse
// (manager.rs:2505-2527), so for any well-formed file the 8-day rule is
// unreachable and last_refresh is never consulted. Stamping a fresh one would
// be inventing a fact to suppress behaviour that does not occur, which is
// exactly the sort of exception that makes derivation unsafe.
func TestSandboxFileLeavesTheRefreshTimestampVerbatim(t *testing.T) {
	t.Parallel()

	file, err := parseCredentialFile(seedFile(t, "an-access-token", "the-refresh-token"))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	sandbox, err := file.accessOnly()
	if err != nil {
		t.Fatalf("accessOnly: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(sandbox.Reveal(), &got); err != nil {
		t.Fatalf("the sandbox file is not JSON: %v", err)
	}
	if got[keyLastRefresh] != "2026-07-01T00:00:00Z" {
		t.Errorf("last_refresh = %#v, want it carried verbatim", got[keyLastRefresh])
	}
}

func TestCredentialFileRewritesOnlyTheTokensAndTheRefreshTimestamp(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	file, err := parseCredentialFile(seedFile(t, "old-access", "old-refresh"))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	_, rotated, err := file.withRotation(Refreshed{
		AccessToken:  work.NewCredential("new-access"),
		RefreshToken: work.NewCredential("new-refresh"),
		IDToken:      work.NewCredential("new-id"),
	}, now)
	if err != nil {
		t.Fatalf("withRotation: %v", err)
	}

	var got struct {
		Tokens struct {
			Access    string `json:"access_token"`
			Refresh   string `json:"refresh_token"`
			ID        string `json:"id_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
		LastRefresh string `json:"last_refresh"`
	}
	if err := json.Unmarshal(rotated, &got); err != nil {
		t.Fatalf("the rotated file is not JSON: %v", err)
	}
	if got.Tokens.Access != "new-access" || got.Tokens.Refresh != "new-refresh" || got.Tokens.ID != "new-id" {
		t.Errorf("rotated tokens = %+v, want all three replaced", got.Tokens)
	}
	if got.Tokens.AccountID != "acct_stored" {
		t.Errorf("account_id = %q, want it untouched", got.Tokens.AccountID)
	}
	// RFC3339 UTC on the wire, from the injected clock and nowhere else.
	if got.LastRefresh != "2026-07-28T09:30:00Z" {
		t.Errorf("last_refresh = %q, want the injected clock's time in RFC3339 UTC", got.LastRefresh)
	}
}

func TestCredentialFileKeepsTheStoredIDTokenWhenARotationOmitsOne(t *testing.T) {
	t.Parallel()
	file, err := parseCredentialFile(seedFile(t, "old-access", "old-refresh"))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	_, rotated, err := file.withRotation(Refreshed{
		AccessToken:  work.NewCredential("new-access"),
		RefreshToken: work.NewCredential("new-refresh"),
	}, time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("withRotation: %v", err)
	}

	var got struct {
		Tokens struct {
			ID string `json:"id_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(rotated, &got); err != nil {
		t.Fatalf("the rotated file is not JSON: %v", err)
	}
	if got.Tokens.ID != "stored-id-token" {
		t.Errorf("id_token = %q, want the stored one kept when the response omits it", got.Tokens.ID)
	}
}

func TestCredentialFileKeepsTheStoredRefreshTokenWhenARotationOmitsOne(t *testing.T) {
	t.Parallel()
	// Every field of the token response is optional and an absent one means
	// unchanged. Blanking the refresh token on the strength of its absence
	// would be the dead credential — the stored one is still the live one.
	file, err := parseCredentialFile(seedFile(t, "old-access", "old-refresh"))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	_, rotated, err := file.withRotation(Refreshed{AccessToken: work.NewCredential("new-access")},
		time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("withRotation: %v", err)
	}

	next, err := parseCredentialFile(rotated)
	if err != nil {
		t.Fatalf("the rotated file no longer parses: %v", err)
	}
	if next.access.Reveal() != "new-access" {
		t.Error("the rotated access token was not written")
	}
	if next.refresh.Reveal() != "old-refresh" {
		t.Error("the stored refresh token was replaced by an omitted one; the credential is now dead")
	}
}

func TestCredentialFileRejectsAFileItCannotUse(t *testing.T) {
	t.Parallel()
	cases := map[string][]byte{
		"a file that is not JSON":        []byte("{{{"),
		"a file with no tokens object":   []byte(`{"OPENAI_API_KEY": "x"}`),
		"a file with no access token":    []byte(`{"tokens":{"refresh_token":"r"}}`),
		"a file with an empty access":    []byte(`{"tokens":{"access_token":"","refresh_token":"r"}}`),
		"a tokens object that is a list": []byte(`{"tokens":[]}`),
		// A blanked refresh token is the shape handed to a sandbox. If the
		// worker ever reads one it has been given a sandbox's copy, and
		// refreshing from it would present an empty string to the provider.
		"a file with a blanked refresh": []byte(`{"tokens":{"access_token":"a","refresh_token":""}}`),
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := parseCredentialFile(stored)
			if !errors.Is(err, ErrUnseeded) {
				t.Fatalf("parseCredentialFile returned %v, want it to wrap ErrUnseeded", err)
			}
			if !errors.Is(err, work.ErrPermanent) {
				t.Error("an unseeded credential must be permanent — a retry cannot seed it")
			}
		})
	}
}

// This test guards the OUTCOME — no token in a rendered string — across a JSON
// handler, four fmt verbs and the error-wrapping path. It is the broader of the
// two guards and it is not going anywhere.
//
// It does NOT catch a LogValue whose signature has drifted off slog.LogValuer.
// Measured: with LogValue() any on both types, this test still passes, because
// slog falls through to fmt and fmt finds String(). That regression is caught
// by the package-scope assertions in authfile.go and refresh.go and by
// TestSlogResolvesTheCredentialHoldersThroughLogValue in redaction_test.go.
// A reader auditing "is the credential covered?" needs both.
func TestCredentialFileNeverPrintsATokenWhenFormattedOrLogged(t *testing.T) {
	t.Parallel()
	const access, refresh = "SECRET-ACCESS-VALUE", "SECRET-REFRESH-VALUE"

	file, err := parseCredentialFile(seedFile(t, access, refresh))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}
	res := Refreshed{
		AccessToken:  work.NewCredential(access),
		RefreshToken: work.NewCredential(refresh),
		IDToken:      work.NewCredential("SECRET-ID-VALUE"),
	}

	var logged strings.Builder
	log := slog.New(slog.NewJSONHandler(&logged, nil))
	log.Info("a stray log line", "file", file, "refreshed", res, "wrapped", fmt.Errorf("context: %w", errRefreshedForTest(res)))

	rendered := []string{logged.String()}
	for _, verb := range []string{"%v", "%+v", "%s", "%q"} {
		rendered = append(rendered, fmt.Sprintf(verb, file), fmt.Sprintf(verb, res))
	}
	for _, out := range rendered {
		for _, secret := range []string{access, refresh, "SECRET-ID-VALUE"} {
			if strings.Contains(out, secret) {
				t.Fatalf("a token reached a rendered string; one %%v away from Loki")
			}
		}
	}
}

// errRefreshedForTest wraps a Refreshed into an error the way a careless caller
// might, so the leak test covers the error path too.
func errRefreshedForTest(r Refreshed) error { return fmt.Errorf("refreshing gave %v", r) }
