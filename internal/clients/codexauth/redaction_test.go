package codexauth

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// This file guards the MECHANISM of redaction for the two types in this package
// that hold the live codex credential. The outcome — that no token reaches a
// rendered string — is already guarded, and well, by
// TestCredentialFileNeverPrintsATokenWhenFormattedOrLogged in authfile_test.go.
//
// The two are not the same property, and that is the whole point of this file.
// Both types declare LogValue() slog.Value correctly today, but nothing stopped
// the next edit turning either into LogValue() any — a signature that compiles,
// reads identically, and does not satisfy slog.LogValuer, so slog never calls
// it at all. That exact regression shipped once on work.Credential (#362) and
// was invisible for the length of its life, because the outcome test cannot see
// it: with LogValue() any, slog falls through to fmt, fmt finds String(), and
// no token appears. Measured on both types here, not assumed.
//
// So the outcome test passes on the bug and the tests below fail on it. Neither
// replaces the other.
//
// The synthetic tokens follow this package's existing rule: no
// credential-shaped string that ever authenticated anything enters the
// repository.
const (
	syntheticAccessToken  = "not-a-real-access-token"
	syntheticRefreshToken = "not-a-real-refresh-token"
	syntheticIDToken      = "not-a-real-id-token"
)

func TestTheCredentialHoldersSatisfyTheInterfaceSlogActuallyUses(t *testing.T) {
	t.Parallel()

	// The load-bearing assertions are at package scope in authfile.go and
	// refresh.go, so a signature that drifts off slog.LogValuer fails
	// `go build` rather than only `go test`. They are restated here because a
	// bare `var _` with nothing beside it reads as dead code to someone tidying
	// up, and deleting it silently removes the guard.
	var (
		_ slog.LogValuer = credentialFile{}
		_ slog.LogValuer = Refreshed{}
	)
}

func TestSlogResolvesTheCredentialHoldersThroughLogValue(t *testing.T) {
	t.Parallel()

	// Resolve is how slog itself unwraps an attribute: it invokes LogValue on a
	// LogValuer and leaves anything else as KindAny. This is not a proxy for
	// the path a handler takes — it is that path. A value that stays KindAny is
	// one slog is treating as an opaque struct, which means LogValue is never
	// called and redaction rests on a fallback instead of on this type.
	//
	// This is the run-time half of the guard, and the half that fails on the
	// regression.
	file, err := parseCredentialFile(seedFile(t, syntheticAccessToken, syntheticRefreshToken))
	if err != nil {
		t.Fatalf("parseCredentialFile: %v", err)
	}

	for name, value := range map[string]any{
		"credentialFile": file,
		"Refreshed": Refreshed{
			AccessToken:  work.NewCredential(syntheticAccessToken),
			RefreshToken: work.NewCredential(syntheticRefreshToken),
			IDToken:      work.NewCredential(syntheticIDToken),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resolved := slog.AnyValue(value).Resolve()

			if resolved.Kind() == slog.KindAny {
				t.Errorf("slog left %s unresolved (kind %s); LogValue is never called on it, so redaction rests on a fallback rather than on this type", name, resolved.Kind())
			}
			// Kind alone is not enough: a LogValue returning the token as a
			// slog.StringValue resolves to KindString and leaks.
			for _, token := range []string{syntheticAccessToken, syntheticRefreshToken, syntheticIDToken} {
				if strings.Contains(resolved.String(), token) {
					t.Errorf("the resolved %s carries a token", name)
				}
			}
		})
	}
}
