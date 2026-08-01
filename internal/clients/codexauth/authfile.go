package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

// The keys of the codex CLI's own credential file, as observed on codex-cli
// 0.145.0 — the version ADR-0011 verified against. They are its format, not
// ours, which is why the file is patched in place rather than re-marshalled
// from a struct: the same file also carries OPENAI_API_KEY and auth_mode,
// which this service does not model and must not drop.
const (
	keyTokens       = "tokens"
	keyAccessToken  = "access_token"
	keyRefreshToken = "refresh_token"
	keyIDToken      = "id_token"
	keyAccountID    = "account_id"
	keyLastRefresh  = "last_refresh"
)

// credentialFile is the stored credential, parsed at the boundary and losslessly.
//
// raw and tokens hold every key of the file as it was read, including ones this
// service has no model for; the Credentials beside them are the three fields it
// does. Rotation writes back through the maps, so an unmodelled field is
// preserved by construction rather than by remembering to preserve it.
type credentialFile struct {
	raw     map[string]json.RawMessage
	tokens  map[string]json.RawMessage
	access  work.Credential
	refresh work.Credential
}

// parseCredentialFile turns the stored bytes into a usable credential, or says
// why they are not one.
//
// Every rejection here is ErrUnseeded and therefore permanent, because every
// one of them describes a file only a human can fix. A blanked refresh token is
// among them: that is the derived access-only shape, and durable storage
// holding one has been given the wrong copy.
func parseCredentialFile(data []byte) (credentialFile, error) {
	if len(data) == 0 {
		return credentialFile{}, fmt.Errorf("the %s key is absent or empty: %w", CredentialKey, ErrUnseeded)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return credentialFile{}, fmt.Errorf("the %s key does not hold a JSON object: %w", CredentialKey, ErrUnseeded)
	}
	tokensRaw, ok := raw[keyTokens]
	if !ok {
		return credentialFile{}, fmt.Errorf("%s carries no %q object: %w", CredentialKey, keyTokens, ErrUnseeded)
	}
	var tokens map[string]json.RawMessage
	if err := json.Unmarshal(tokensRaw, &tokens); err != nil {
		return credentialFile{}, fmt.Errorf("%s's %q is not a JSON object: %w", CredentialKey, keyTokens, ErrUnseeded)
	}

	access, err := stringField(tokens, keyAccessToken)
	if err != nil {
		return credentialFile{}, err
	}
	refresh, err := stringField(tokens, keyRefreshToken)
	if err != nil {
		return credentialFile{}, err
	}
	return credentialFile{
		raw:     raw,
		tokens:  tokens,
		access:  work.NewCredential(access),
		refresh: work.NewCredential(refresh),
	}, nil
}

// stringField reads one required token field, refusing an empty one. The value
// never reaches the error message.
func stringField(tokens map[string]json.RawMessage, key string) (string, error) {
	rawValue, ok := tokens[key]
	if !ok {
		return "", fmt.Errorf("%s's %s.%s is absent: %w", CredentialKey, keyTokens, key, ErrUnseeded)
	}
	var value string
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return "", fmt.Errorf("%s's %s.%s is not a string: %w", CredentialKey, keyTokens, key, ErrUnseeded)
	}
	if value == "" {
		return "", fmt.Errorf("%s's %s.%s is blank: %w", CredentialKey, keyTokens, key, ErrUnseeded)
	}
	return value, nil
}

// withRotation returns the stored file with the rotated pair patched into it,
// both as a parsed file and as the bytes to store.
//
// It patches four fields and copies everything else, so the file the CLI wrote
// stays the file the CLI wrote. An omitted id_token leaves the stored one
// alone: the provider is not obliged to reissue one, and blanking a field on
// the strength of its absence from one response would be inventing a fact.
//
// The parsed file comes back too because the caller must derive the access-only
// copy from the ROTATED document, not the one that was read. Re-parsing the
// bytes would work and is what an earlier shape did, but it would also re-run
// the boundary's rejections against a document this package just built.
func (f credentialFile) withRotation(res Refreshed, now time.Time) (credentialFile, []byte, error) {
	raw := make(map[string]json.RawMessage, len(f.raw))
	for k, v := range f.raw {
		raw[k] = v
	}
	tokens := make(map[string]json.RawMessage, len(f.tokens))
	for k, v := range f.tokens {
		tokens[k] = v
	}

	set := func(key string, value work.Credential) error {
		encoded, err := json.Marshal(value.Reveal())
		if err != nil {
			return fmt.Errorf("encoding the rotated %s.%s: %w", keyTokens, key, err)
		}
		tokens[key] = encoded
		return nil
	}
	// An omitted field means unchanged, never blank. The provider is not
	// obliged to reissue all three, and blanking the refresh token on the
	// strength of its absence from one response is how a live credential
	// becomes a dead one.
	for key, value := range map[string]work.Credential{
		keyAccessToken:  res.AccessToken,
		keyRefreshToken: res.RefreshToken,
		keyIDToken:      res.IDToken,
	} {
		if value.Reveal() == "" {
			continue
		}
		if err := set(key, value); err != nil {
			return credentialFile{}, nil, err
		}
	}

	// RFC3339Nano rather than RFC3339, to match what the CLI itself writes:
	// observed on codex-cli 0.145.0, last_refresh carries subsecond precision.
	// Whole seconds still render without a fraction, so this only ever adds
	// digits the CLI would have written anyway.
	encodedNow, err := json.Marshal(now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return credentialFile{}, nil, fmt.Errorf("encoding the refresh timestamp: %w", err)
	}
	raw[keyLastRefresh] = encodedNow

	// raw[keyTokens] is deliberately STALE here: it still holds the
	// pre-rotation blob, spent refresh token and all. tokens is the current
	// one, and encode is what reconciles the two — it re-marshals tokens over
	// raw[keyTokens] on the way out. Nothing else may read raw[keyTokens] off
	// this value, and accessOnly does not: it passes raw through and supplies
	// its own tokens map, so encode reconciles there too.
	rotated := credentialFile{
		raw:     raw,
		tokens:  tokens,
		access:  pick(res.AccessToken, f.access),
		refresh: pick(res.RefreshToken, f.refresh),
	}
	out, err := rotated.encode()
	if err != nil {
		return credentialFile{}, nil, fmt.Errorf("encoding the rotated credential file: %w", err)
	}
	return rotated, out, nil
}

// pick keeps the stored value when a rotation omitted its replacement, matching
// the patch rule above so the parsed fields never disagree with the maps.
func pick(next, stored work.Credential) work.Credential {
	if next.Reveal() == "" {
		return stored
	}
	return next
}

// encode renders the file, folding the token map back into the document.
func (f credentialFile) encode() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(f.raw))
	for k, v := range f.raw {
		raw[k] = v
	}
	encodedTokens, err := json.Marshal(f.tokens)
	if err != nil {
		return nil, fmt.Errorf("encoding the %q object: %w", keyTokens, err)
	}
	raw[keyTokens] = encodedTokens

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encoding the credential file: %w", err)
	}
	return out, nil
}

// accessOnly returns the document for the main-worker adapter with the refresh
// token blanked.
//
// It is a DERIVATION of the stored file rather than a document composed from
// parts, and that is the safety property. Composition is correct only while
// somebody's list of required fields stays complete and current — and those
// fields' serde attributes are not uniform: id_token is mandatory and
// JWT-parsed, OPENAI_API_KEY must be present but may be null, refresh_token
// must be present but may be blank, everything else is omissible. Derived,
// every key survives because nothing enumerated them, so a future codex release
// adding a mandatory field breaks nothing. last_refresh is carried verbatim for
// the same reason; the CLI does not read it for a well-formed file.
//
// The refresh token is SET TO THE EMPTY STRING. It is never removed and never
// nulled. On codex-cli rust-v0.145.0, TokenData.refresh_token is a bare String
// with no Option, no serde(default) and no custom deserializer, so a blank
// value parses while an absent key or a null fails the entire document.
func (f credentialFile) accessOnly() (work.CredentialFile, error) {
	tokens := make(map[string]json.RawMessage, len(f.tokens))
	for k, v := range f.tokens {
		tokens[k] = v
	}
	tokens[keyRefreshToken] = json.RawMessage(`""`)

	out, err := credentialFile{raw: f.raw, tokens: tokens}.encode()
	if err != nil {
		return work.CredentialFile{}, fmt.Errorf("building the access-only credential file: %w", err)
	}
	return work.NewCredentialFile(out), nil
}

// String redacts the whole file. Its fields are Credentials already, but the
// raw maps are not, so without this a %v would print the stored token bytes.
func (f credentialFile) String() string { return "[redacted codex credential file]" }

// credentialFile must satisfy slog.LogValuer, enforced here at package scope so
// a signature that drifts off the interface fails `go build` rather than only
// `go test`. LogValue() any compiles, reads exactly like the method below, and
// is never called by slog at all — see work.Credential.LogValue (#362).
var _ slog.LogValuer = credentialFile{}

// LogValue redacts the file in structured logs.
func (f credentialFile) LogValue() slog.Value { return slog.StringValue(f.String()) }

// expiryOf reads the exp claim from an access token without verifying its
// signature.
//
// We are not authenticating the token — the provider does that on use — only
// reading the lifetime it declares, so verification would buy nothing and need
// a key we do not have.
//
// Every failure is an error rather than "assume expired". Assuming expired
// would refresh, fail to read the new token in exactly the same way, and burn
// the whole rotating chain in a loop.
func expiryOf(accessToken work.Credential) (time.Time, error) {
	segments := strings.Split(accessToken.Reveal(), ".")
	if len(segments) != 3 {
		return time.Time{}, fmt.Errorf("the access token is not three dot-separated segments, so its expiry cannot be read")
	}
	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("the access token's payload is not base64url, so its expiry cannot be read")
	}
	var claims struct {
		Exp *int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("the access token's payload is not JSON with a numeric exp claim, so its expiry cannot be read")
	}
	if claims.Exp == nil || *claims.Exp <= 0 {
		return time.Time{}, fmt.Errorf("the access token carries no usable exp claim, so its expiry cannot be read")
	}
	return time.Unix(*claims.Exp, 0).UTC(), nil
}
