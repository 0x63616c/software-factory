// Package clocktest provides a Clock whose time only moves when a test moves
// it. It exists so no test has to sleep: a poll interval, a breaker cooldown
// and a token expiry are all durations this service waits out, and waiting them
// out for real would make the suite slow and flaky in equal measure.
package clocktest

import (
	"context"
	"sync"
	"time"
)

// Fake is a controllable Clock.
//
// Its Sleep returns immediately, advancing the fake time instead of waiting, so
// a test exercising a retry loop finishes in microseconds while still seeing
// the timestamps the code under test would have seen. It is safe for concurrent
// use, because the code under test is allowed to be.
type Fake struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

// NewFake returns a Fake started at the given time.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start.UTC()}
}

// Now returns the current fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake time forward without recording a sleep.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Sleep advances the fake time by d and returns immediately, recording the
// duration. A cancelled context still wins, so code that must honour
// cancellation can be tested for it.
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if d > 0 {
		f.now = f.now.Add(d)
		f.slept = append(f.slept, d)
	}
	return nil
}

// Slept returns the durations passed to Sleep, in order. Assert on this to pin
// a backoff schedule without waiting for one.
func (f *Fake) Slept() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.slept...)
}
