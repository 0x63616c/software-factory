// Package clock is the only place in this service that reads wall-clock time.
// Polling, circuit-breaker cooldown and access-token expiry are all
// time-driven and all must be testable, so everything that needs the time
// takes a Clock and a test hands it a fake one.
//
// Workflow code is the exception, and does not use this package: inside
// internal/workflows the answer is workflow.Now and workflow.Sleep, because
// only Temporal's clock survives replay.
package clock

import (
	"context"
	"time"
)

// Clock reads the current time and waits. Two methods, because those are the
// only two things this service does with time.
type Clock interface {
	// Now returns the current time in UTC.
	Now() time.Time

	// Sleep waits for d, or returns ctx.Err() if the context is cancelled
	// first. It never blocks past cancellation, which is what makes a
	// long-running poll or cooldown promptly stoppable.
	Sleep(ctx context.Context, d time.Duration) error
}
