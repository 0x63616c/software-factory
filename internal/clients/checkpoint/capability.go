package checkpoint

import (
	"encoding/base64"
	"fmt"
	"io"

	"github.com/0x63616c/software-factory/internal/work"
)

const capabilityBytes = 32

// CapabilityMinter creates opaque checkpoint capabilities from an injected
// entropy source. Production supplies crypto/rand.Reader; tests stay exact.
type CapabilityMinter struct{ random io.Reader }

// NewCapabilityMinter builds a capability minter from an injected entropy source.
func NewCapabilityMinter(random io.Reader) (*CapabilityMinter, error) {
	if random == nil {
		return nil, fmt.Errorf("constructing checkpoint capability minter: random source is nil")
	}
	return &CapabilityMinter{random: random}, nil
}

// Mint returns a new opaque capability that refuses JSON serialization.
func (m *CapabilityMinter) Mint() (work.Credential, error) {
	raw := make([]byte, capabilityBytes)
	if _, err := io.ReadFull(m.random, raw); err != nil {
		return work.Credential{}, fmt.Errorf("reading checkpoint capability entropy: %w", err)
	}
	return work.NewCredential(base64.RawURLEncoding.EncodeToString(raw)), nil
}
