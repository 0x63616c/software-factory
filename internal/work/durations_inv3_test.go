package work_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/clients/codexauth"
	"github.com/0x63616c/software-factory/internal/clients/codexauth/storefake"
	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/work"
)

// refresherStub stands in for the one network call in codexauth. Nothing here
// calls it: this file constructs a Source and throws it away.
type refresherStub struct{}

func (refresherStub) Refresh(context.Context, work.Credential) (codexauth.Refreshed, codexauth.RefreshOutcome, error) {
	return codexauth.Refreshed{}, codexauth.RefreshNotSent, nil
}

// TestMaxStageDurationSatisfiesTheCredentialMargin is INV-3, checked by
// construction rather than by arithmetic.
//
// A sandbox is handed a copy of the credential that it cannot refresh, so that
// copy must outlive the whole stage plus the five-minute window in which the
// CLI would start trying. codexauth enforces that in its constructor against
// values it keeps to itself, so the only honest way to ask "does our stage
// length still fit?" is to hand it the constant and see.
//
// Live evidence for why this matters, from #340: when a sandbox does reach
// that window, codex exec does not fail once — it exits 1 having made 104
// requests to the auth endpoint in 35 seconds, per stage, from one egress IP.
func TestMaxStageDurationSatisfiesTheCredentialMargin(t *testing.T) {
	t.Parallel()

	_, err := codexauth.New(
		storefake.New(nil),
		refresherStub{},
		clocktest.NewFake(time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"durations-test",
		work.MaxStageDuration,
	)
	if err != nil {
		t.Fatalf("a token source cannot be built for a stage of %s: %v", work.MaxStageDuration, err)
	}
}

// TestAStageLongerThanTheMarginIsRefused proves the check above can fail: a
// green test that would also pass with the invariant broken proves nothing.
func TestAStageLongerThanTheMarginIsRefused(t *testing.T) {
	t.Parallel()

	_, err := codexauth.New(
		storefake.New(nil),
		refresherStub{},
		clocktest.NewFake(time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"durations-test",
		24*time.Hour,
	)
	if err == nil {
		t.Fatal("a token source was built for a 24-hour stage; INV-3 is not being enforced, so the test above proves nothing")
	}
}
