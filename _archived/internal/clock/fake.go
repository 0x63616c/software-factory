package clock

import "time"

// Fake is a deterministic Clock for tests. It never moves on its own — Advance drives
// it forward explicitly — so tests are fully reproducible (SoftwareStyle testability
// floor: no real time).
type Fake struct {
	current time.Time
}

// NewFake returns a Fake pinned to the given instant, normalized to UTC to match the
// Clock contract (the engine is UTC-only).
func NewFake(t time.Time) *Fake { return &Fake{current: t.UTC()} }

// Now returns the Fake's current instant without advancing it.
func (f *Fake) Now() time.Time { return f.current }

// Advance moves the Fake's clock forward by d.
func (f *Fake) Advance(d time.Duration) { f.current = f.current.Add(d) }
