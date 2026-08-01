package codexauth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestRefreshStateTreatsAnAbsentStateAsAnUnattemptedCredential(t *testing.T) {
	t.Parallel()
	// The seam cannot remove a key, only blank one, so "absent" and "blank"
	// are the same fact and both mean a credential nobody has touched.
	for name, stored := range map[string][]byte{"absent": nil, "blank": {}, "empty object": []byte(`{}`)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state, err := parseRefreshState(stored)
			if err != nil {
				t.Fatalf("parseRefreshState: %v", err)
			}
			if state.Serial != 0 || state.Attempt != nil {
				t.Errorf("state = %+v, want serial 0 and no attempt", state)
			}
		})
	}
}

func TestRefreshStateRefusesToReasonAboutAnUnparseableState(t *testing.T) {
	t.Parallel()
	// Not knowing whether a refresh is in flight is exactly the condition
	// under which presenting the token is unsafe, so this cannot degrade to
	// "assume nobody is refreshing".
	_, err := parseRefreshState([]byte("{{{"))
	if !errors.Is(err, ErrUnseeded) {
		t.Fatalf("parseRefreshState returned %v, want it to wrap ErrUnseeded", err)
	}
	if !errors.Is(err, work.ErrPermanent) {
		t.Error("an unreadable lease state must be permanent — a retry reads the same bytes")
	}
}

func TestRefreshStateRoundTripsThroughItsEncoding(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 28, 11, 3, 12, 0, time.UTC)
	want := refreshState{
		Serial:     7,
		LastWriter: "worker-6c9f/1a2b",
		Attempt: &attempt{
			Holder:         "worker-6c9f/1a2b",
			StartedAt:      started,
			LeaseExpiresAt: started.Add(5 * time.Minute),
			Serial:         7,
			TakeoverOf:     "worker-4d1e/9c0a",
			Outcome:        outcomeRejected,
		},
	}

	encoded, err := encodeRefreshState(want)
	if err != nil {
		t.Fatalf("encodeRefreshState: %v", err)
	}
	got, err := parseRefreshState(encoded)
	if err != nil {
		t.Fatalf("parseRefreshState: %v", err)
	}
	if got.Serial != want.Serial || got.LastWriter != want.LastWriter || got.Attempt == nil {
		t.Fatalf("state = %+v, want %+v", got, want)
	}
	if *got.Attempt != *want.Attempt {
		t.Errorf("attempt = %+v, want %+v", *got.Attempt, *want.Attempt)
	}
	if !strings.Contains(string(encoded), "2026-07-28T11:03:12Z") {
		t.Errorf("encoded state = %s, want RFC3339 UTC timestamps", encoded)
	}
}

func TestRefreshStateWritesNoSecretMaterial(t *testing.T) {
	t.Parallel()
	// The lease lives in the same Secret as the credential, so it is worth
	// pinning that it carries only identifiers and times. Everything it holds
	// is chosen by this package; nothing is copied from a token.
	encoded, err := encodeRefreshState(refreshState{
		Serial:     8,
		LastWriter: "worker-6c9f/1a2b",
		Attempt: &attempt{
			Holder:         "worker-6c9f/1a2b",
			StartedAt:      time.Unix(0, 0).UTC(),
			LeaseExpiresAt: time.Unix(300, 0).UTC(),
			Serial:         8,
		},
	})
	if err != nil {
		t.Fatalf("encodeRefreshState: %v", err)
	}
	for _, field := range []string{keyAccessToken, keyRefreshToken, keyIDToken} {
		if strings.Contains(string(encoded), field) {
			t.Errorf("the lease state mentions %q; it must carry no token material at all", field)
		}
	}
}
