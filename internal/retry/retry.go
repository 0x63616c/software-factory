package retry

import (
	"context"
	"math"
	"time"

	"github.com/0x63616c/software-factory/internal/clock"
)

// Policy controls exponential retry timing.
//
// InitialDelay must be positive. When Multiplier is 1 or less, each retry uses
// InitialDelay.
type Policy struct {
	InitialDelay time.Duration
	Multiplier   float64
	MaxDelay     time.Duration
}

// Delay returns the attempt-th retry delay.
func (p Policy) Delay(attempt int) time.Duration {
	if attempt <= 0 || p.InitialDelay <= 0 {
		return p.InitialDelay
	}
	if p.Multiplier <= 1 {
		return p.InitialDelay
	}

	delay := time.Duration(float64(p.InitialDelay) * math.Pow(p.Multiplier, float64(attempt)))
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}

// Sleep waits for the configured delay for the attempt-th retry.
func (p Policy) Sleep(ctx context.Context, c clock.Clock, attempt int) error {
	return c.Sleep(ctx, p.Delay(attempt))
}

// Retry executes `step` up to maxAttempts times. For each failed attempt,
// it sleeps for the attempt-th backoff delay before the next attempt.
//
// `step` returns whether another attempt should be made and the error observed.
func Retry(ctx context.Context, c clock.Clock, maxAttempts int, backoff Policy, step func(context.Context, int) (bool, error)) error {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		shouldRetry, err := step(ctx, attempt)
		if !shouldRetry {
			return err
		}
		if attempt+1 >= maxAttempts {
			return err
		}
		if err := backoff.Sleep(ctx, c, attempt); err != nil {
			return err
		}
	}

	return nil
}
