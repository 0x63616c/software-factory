package retry

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
)

// Sleeper models the minimal clock interface needed for retrying with backoff.
type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

// Attempt is one retryable unit of work.
//
// retry indicates whether the caller should try again.
// err is the attempt's outcome. When retry is false, err is returned directly by
// RetryWithBackoff.
type Attempt func(context.Context, int) (retry bool, err error)

// RetryWithBackoff retries attempt until a terminal outcome or attempts exhausted.
//
// `attempt` is called with a zero-based index.
func RetryWithBackoff(ctx context.Context, clock Sleeper, maxAttempts int, nextDelay func(int) time.Duration, attempt Attempt) error {
	if ctx == nil {
		return errors.New("retry context is required")
	}
	if clock == nil {
		return errors.New("retry clock is required")
	}
	if maxAttempts <= 0 {
		return errors.New("retry attempts must be greater than zero")
	}
	if attempt == nil {
		return errors.New("retry attempt function is required")
	}

	for i := 0; i < maxAttempts; i++ {
		retry, err := attempt(ctx, i)
		if !retry {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if i == maxAttempts-1 {
			return err
		}
		delay := nextDelay(i)
		if delay <= 0 {
			continue
		}
		if err := clock.Sleep(ctx, delay); err != nil {
			return err
		}
	}
	return nil
}
