// Package clock provides an injectable time source. Reading the current time is an
// external edge, and SoftwareStyle makes testability a floor: no unit test may touch
// the real clock. So time-dependent code depends on Clock (a narrow seam) and is
// handed System in production and Fake in tests.
//
// To keep this honest, `time.Now` is banned outside this package by golangci-lint
// (see .golangci.yml) — the same "one door" pattern as os.Getenv in internal/config.
package clock

import "time"

// Clock is the narrow seam every time-dependent component depends on.
type Clock interface {
	// Now returns the current instant, ALWAYS in UTC. The engine is UTC-only
	// (SoftwareStyle time standard); localization happens solely in the TUI.
	Now() time.Time
}

// System is the real clock, backed by the operating system. Wire it at the
// composition root; never reach for time.Now directly.
type System struct{}

// Now returns the current wall-clock time in UTC. Because this is the only door to
// the real clock (time.Now is linter-banned elsewhere), the whole engine is
// guaranteed UTC — you cannot accidentally introduce a local-zone timestamp.
func (System) Now() time.Time { return time.Now().UTC() }
