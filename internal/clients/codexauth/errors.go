package codexauth

import (
	"errors"
	"fmt"

	"github.com/0x63616c/software-factory/internal/work"
)

// The credential's failure modes. Each one that a retry cannot fix wraps
// work.ErrPermanent, so callers ask that one question — errors.Is(err,
// work.ErrPermanent) — rather than a predicate this package would have to keep
// in step with its own sentinel list. Anything not listed here is retryable,
// which is Temporal's default and this package's.
//
// The messages are written for the person reading them at 3am with no context,
// because for most of these the next step is a human running a browser login.
var (
	// ErrUnseeded means no usable credential is stored. Seeding is a human
	// step by design: the refresh token rotates on first use, so a value in
	// git or in a Pulumi stack is a corpse within a day.
	ErrUnseeded = fmt.Errorf("codex credential is not seeded: %w", work.ErrPermanent)

	// ErrRefreshRejected means the provider refused the refresh token. It is
	// spent or revoked, and no amount of asking again changes that.
	ErrRefreshRejected = fmt.Errorf("codex refresh token was rejected: %w", work.ErrPermanent)

	// ErrRefreshOutcomeUnknown means a refresh token reached the provider and
	// no usable answer came back, so it may already be spent.
	//
	// This is the sentinel that costs availability on purpose. Presenting a
	// possibly-spent token again is the one action that can destroy a live
	// credential, so an unknown outcome halts instead of retrying, and the
	// attempt marker it leaves behind is what stops the next process too.
	ErrRefreshOutcomeUnknown = fmt.Errorf("codex refresh outcome is unknown: %w", work.ErrPermanent)

	// ErrSingleWriterViolated means something other than this Source rotated
	// the credential. Investigate who before re-seeding; a second writer will
	// simply do it again.
	ErrSingleWriterViolated = fmt.Errorf("codex credential was rotated by another writer: %w", work.ErrPermanent)

	// ErrCredentialLost means a rotation succeeded and the new pair could not
	// be stored. The token in hand is good for days while the stored refresh
	// token is already dead, so this fails the call rather than returning it:
	// a system that works perfectly unattended and then bricks with nobody
	// watching is the expensive way to find out.
	ErrCredentialLost = fmt.Errorf("rotated codex credential could not be stored: %w", work.ErrPermanent)

	// ErrRefreshTooShortLived means a rotation returned a token that already
	// falls inside the refresh margin, so returning it would hand the model
	// adapter a credential that can expire during its bounded operation. The
	// rotated pair is stored before this is returned — dropping it would spend
	// a refresh token for nothing.
	ErrRefreshTooShortLived = fmt.Errorf("rotated codex access token expires too soon to use: %w", work.ErrPermanent)

	// ErrRefreshInProgress means another holder holds the lease and had not
	// finished within our wait. Nothing was presented, so this is safe to
	// retry — and deliberately not permanent, because it is the ordinary
	// outcome of two callers wanting the same expired token at once.
	ErrRefreshInProgress = errors.New("another holder is refreshing the codex credential")
)
