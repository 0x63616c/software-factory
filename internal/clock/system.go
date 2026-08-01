package clock

import (
	"context"
	"time"
)

// System is the real clock. Only the composition root constructs one; every
// other package takes a Clock so a test can hand it a fake instead.
type System struct{}

// Now returns the current time in UTC. This service is UTC-only, so the
// conversion happens here rather than at every call site that formats a time.
func (System) Now() time.Time {
	return time.Now().UTC()
}

// Sleep waits for d, or returns the context's error if it is cancelled first.
//
// It uses a timer rather than time.Sleep because a bare sleep cannot be
// cancelled: a poll loop or a breaker cooldown would keep a shutting-down
// worker alive for its full interval, past the termination grace period.
func (System) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
