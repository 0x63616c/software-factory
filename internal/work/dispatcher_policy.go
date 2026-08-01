package work

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// DispatcherPolicy is the complete resolved admission policy for future target
// Runs. A child receives a copy at admission and never observes a later update.
type DispatcherPolicy struct {
	Run         TargetRunPolicy
	MaxInFlight int
	// Paused prevents new Ticket admission while preserving existing Runs.
	// It is part of the resolved policy so every worker publication has one
	// complete, replay-stable control snapshot.
	Paused bool
}

// DefaultDispatcherPolicy returns the resolved policy published by a target
// worker before it is allowed to poll its main queue.
func DefaultDispatcherPolicy() DispatcherPolicy {
	return DispatcherPolicy{Run: DefaultTargetRunPolicy(), MaxInFlight: 1}
}

// Validate reports whether a DispatcherPolicy can safely admit target Runs.
func (p DispatcherPolicy) Validate() error {
	if err := p.Run.Validate(); err != nil {
		return fmt.Errorf("dispatcher run policy: %w", err)
	}
	if p.MaxInFlight <= 0 {
		return fmt.Errorf("dispatcher maximum in-flight runs must be positive")
	}
	return nil
}

// Fingerprint identifies one complete resolved policy for equality only. It is
// deliberately not an ordering value or Temporal Update ID.
func (p DispatcherPolicy) Fingerprint() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encoding dispatcher policy fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
