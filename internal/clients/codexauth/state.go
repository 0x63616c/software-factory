package codexauth

import (
	"encoding/json"
	"fmt"
	"time"
)

// The two keys of the credential Secret. One Secret rather than two objects,
// because a k8s update is atomic over the whole object: the lease marker and
// the credential it protects therefore land at one linearization point, and a
// lease held while the credential moved is not representable.
const (
	// CredentialKey holds the codex CLI's own credential file, byte-preserved
	// except for the fields a rotation rewrites. Seeding is a straight file
	// copy after a local `codex login`, with no translation step to get wrong
	// at 3am.
	CredentialKey = "auth.json"

	// StateKey holds the lease and the credential's generation counter. It
	// carries no secret material — see the package doc's leak audit.
	StateKey = "refresh_state.json"
)

// outcomeRejected records that the provider refused the token an attempt
// presented. It is the only terminal outcome worth storing: every other one is
// either fine (the credential moved on) or unknown (the attempt stays open,
// which is what the absence of an outcome means).
const outcomeRejected = "rejected"

// refreshState is the lease, stored beside the credential it protects.
//
// Serial is a counter of the stored credential's generation, incremented in the
// same atomic write that stores a rotated credential and nowhere else.
// LastWriter names who performed that increment. Together they are how a writer
// recognises its own write after a lost response — without ever comparing token
// bytes, and without inferring identity from a number two actors could have
// chosen independently.
type refreshState struct {
	Serial     int64    `json:"serial"`
	LastWriter string   `json:"last_writer,omitempty"`
	Attempt    *attempt `json:"attempt,omitempty"`
}

// attempt is one refresh in flight, or one that never settled.
//
// Its presence is the lease. It is written before the refresh token is
// presented and cleared after the result is durably stored, so a process that
// dies in between leaves behind a record saying so — which is the difference
// between "not yet attempted" and "attempted, outcome unknown", and therefore
// the difference between a safe retry and a destroyed credential.
type attempt struct {
	Holder         string    `json:"holder"`
	StartedAt      time.Time `json:"started_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`

	// Serial is the credential generation this attempt is presenting. An
	// attempt whose serial is behind the state's has already been superseded
	// by a rotation and says nothing about the current token.
	Serial int64 `json:"serial"`

	// TakeoverOf names the holder whose expired lease this attempt seized. Its
	// presence is what bounds takeover at one per generation: a second
	// unresolved attempt carrying it halts rather than presenting again.
	TakeoverOf string `json:"takeover_of,omitempty"`

	// Outcome is set only for a known terminal result. Absent means live or
	// unresolved, and those two are told apart by LeaseExpiresAt.
	Outcome string `json:"outcome,omitempty"`
}

// parseRefreshState reads the lease, treating an absent or blank key as a
// credential nobody has attempted.
//
// Unparseable is not the same thing and is fatal: not knowing whether a refresh
// is in flight is exactly the condition under which presenting the token is
// unsafe, so this must never degrade into "assume nobody is refreshing".
func parseRefreshState(data []byte) (refreshState, error) {
	if len(data) == 0 {
		return refreshState{}, nil
	}
	var state refreshState
	if err := json.Unmarshal(data, &state); err != nil {
		return refreshState{}, fmt.Errorf("the %s key is not readable as lease state, so it is not safe to refresh: %w", StateKey, ErrUnseeded)
	}
	return state, nil
}

// encodeRefreshState renders the lease for storage.
func encodeRefreshState(state refreshState) ([]byte, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encoding the %s lease state: %w", StateKey, err)
	}
	return encoded, nil
}

// live reports whether the attempt still holds its lease at now.
func (a *attempt) live(now time.Time) bool {
	return a != nil && now.Before(a.LeaseExpiresAt)
}
