package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
	gh "github.com/google/go-github/v78/github"
)

// Why a failure happened, for the callers that act differently on each. They
// are the only values this package's callers should match on, and they say
// nothing about retrying — that is work.ErrPermanent's one bit, and every
// permanent error below carries it too.
var (
	// ErrAuth means the credential is wrong, revoked, or a grant is missing. No
	// number of retries mints a permission the installation does not hold.
	ErrAuth = errors.New("github refused this app's credentials")

	// ErrRateLimit means a limit was reached. Whether that is fatal depends on
	// which limit: see classify.
	ErrRateLimit = errors.New("github rate limit reached")

	// ErrNotFound means the issue or comment is gone — or, in a private
	// repository, that this installation cannot see it, which GitHub reports
	// the same way.
	ErrNotFound = errors.New("the github resource does not exist")

	// ErrInvalid means we sent something GitHub would not accept. Sending it
	// again sends the same thing.
	ErrInvalid = errors.New("github rejected the request as malformed")

	// ErrRuleset means repository policy or the installation's merge grant
	// currently blocks a valid merge request. It is retryable so the target
	// merge policy can wait for operator repair until its deadline.
	ErrRuleset = errors.New("github repository policy blocks the merge")
)

// defaultRetryAfter is what a rate limit costs when GitHub does not say. Its
// secondary limits are typically a minute.
const defaultRetryAfter = time.Minute

// permanent marks a failure a retry cannot fix, naming both what went wrong and
// what we were doing.
//
// Two sentinels travel in one error deliberately: work.ErrPermanent is the bit
// internal/activities translates into a non-retryable Temporal
// ApplicationError, and the kind is what a caller keying off the *reason*
// matches. Neither is derivable from the other, and a client cannot import the
// Temporal SDK without breaking the seal that keeps an SDK's worldview out of
// the rest of this service.
func permanent(op string, kind, cause error) error {
	return fmt.Errorf("%s: %w (%w): %w", op, kind, work.ErrPermanent, cause)
}

// classify turns a go-github failure into this service's vocabulary.
//
// Every method funnels through it, so the mapping from an HTTP status to a
// verdict exists once. op names the operation and its subject — "clearing the
// auto label on issue #328" — because an error read at 3am in Loki has no stack
// beside it.
func classify(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}

	// Cancellation is not a failure of GitHub's, and Temporal must see it as
	// cancellation rather than as something to retry.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", op, ctxErr)
	}

	// A token exchange that failed inside the API plane's transport surfaces
	// here wrapped in the request that triggered it, already classified. Judging
	// it again by its HTTP status would overwrite a verdict made with more
	// information — the exchange knows a 404 means our installation, not the
	// issue someone was reading — so only the operation is added.
	if alreadyClassified(err) {
		return fmt.Errorf("%s: %w", op, err)
	}

	// The primary limit is 5,000 requests an hour against an installation token
	// and this service makes single-digit calls per ticket, so reaching it means
	// something is wrong. The reset window is up to an hour — longer than any
	// sane activity retry budget — so fail loud rather than retry into it.
	var primary *gh.RateLimitError
	if errors.As(err, &primary) {
		return permanent(op, ErrRateLimit, fmt.Errorf(
			"the primary rate limit resets at %s: %w", primary.Rate.Reset.UTC().Format(time.RFC3339), err))
	}

	// Secondary limits and 429s are typically minute-long transients, and
	// failing the activity for one would fail the whole WorkTicket workflow —
	// discarding every token already spent so far this run to save a
	// minute.
	var secondary *gh.AbuseRateLimitError
	if errors.As(err, &secondary) {
		wait := defaultRetryAfter
		if secondary.RetryAfter != nil {
			wait = *secondary.RetryAfter
		}
		return rateLimited(op, wait, err)
	}

	var resp *gh.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.Response.StatusCode {
		case http.StatusTooManyRequests:
			// go-github's CheckResponse does not special-case 429; it falls
			// through to a plain ErrorResponse, taking its Retry-After with it.
			return rateLimited(op, retryAfterIn(resp.Response.Header), err)
		case http.StatusUnauthorized, http.StatusForbidden:
			return permanent(op, ErrAuth, err)
		case http.StatusNotFound:
			return permanent(op, ErrNotFound, err)
		case http.StatusUnprocessableEntity:
			return permanent(op, ErrInvalid, err)
		}
	}

	// 5xx, a transport failure, a deadline: unmarked, and therefore retryable.
	return fmt.Errorf("%s: %w", op, err)
}

// rateLimited reports a limit worth waiting out. The interval GitHub asked for
// travels in the message rather than in the error's type: nothing consumes it
// yet, and a retry policy is the caller's to set.
func rateLimited(op string, wait time.Duration, cause error) error {
	return fmt.Errorf("%s: %w, github asked us to wait %s: %w", op, ErrRateLimit, wait, cause)
}

// retryAfterIn reads the Retry-After header's delta-seconds form.
//
// Its HTTP-date form is deliberately not parsed: turning a date into a duration
// needs the injected clock, and this value is advisory — it is reported, not
// obeyed — so threading one down here would buy nothing.
func retryAfterIn(h http.Header) time.Duration {
	seconds, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || seconds <= 0 {
		return defaultRetryAfter
	}
	return time.Duration(seconds) * time.Second
}

// alreadyClassified reports whether an error already carries this package's
// vocabulary.
func alreadyClassified(err error) bool {
	return errors.Is(err, ErrAuth) || errors.Is(err, ErrRateLimit) ||
		errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrRuleset)
}
