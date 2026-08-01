package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/clients/codexauth/storefake"
	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/work"
)

// testNow anchors every test's fake clock. Nothing here reads the real one.
var testNow = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)

const (
	testHolder        = "worker-6c9f/1a2b"
	testStageDuration = time.Hour
)

// newFakeStore is a bare store for construction tests.
func newFakeStore(t *testing.T) *storefake.Store { t.Helper(); return storefake.New(nil) }

type harness struct {
	store     *storefake.Store
	refresher *fakeRefresher
	clock     *clocktest.Fake
	metrics   *recordingMetrics
	logs      *strings.Builder
	source    *Source
}

// newHarness builds a Source over an already-seeded store. accessLifetime is
// how far the stored access token is from expiry, so a test says "fresh" or
// "inside the margin" rather than doing arithmetic.
func newHarness(t *testing.T, accessLifetime time.Duration, state refreshState, opts ...Option) *harness {
	t.Helper()
	stateBytes, err := encodeRefreshState(state)
	if err != nil {
		t.Fatalf("seeding the lease state: %v", err)
	}
	seed := map[string][]byte{
		CredentialKey: seedFile(t, unsignedJWT(t, testNow.Add(accessLifetime)), "stored-refresh"),
	}
	if state.Serial != 0 || state.Attempt != nil || state.LastWriter != "" {
		seed[StateKey] = stateBytes
	}
	return newHarnessOver(t, storefake.New(seed), opts...)
}

func newHarnessOver(t *testing.T, store *storefake.Store, opts ...Option) *harness {
	t.Helper()
	h := &harness{
		store:     store,
		refresher: &fakeRefresher{},
		clock:     clocktest.NewFake(testNow),
		metrics:   &recordingMetrics{},
		logs:      &strings.Builder{},
	}
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source, err := New(h.store, h.refresher, h.clock, log, testHolder, testStageDuration, append([]Option{WithMetrics(h.metrics)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.source = source
	return h
}

// rotatesTo scripts one successful rotation whose new access token has the
// given lifetime from testNow.
func (h *harness) rotatesTo(t *testing.T, lifetime time.Duration) Refreshed {
	t.Helper()
	res := Refreshed{
		AccessToken:  work.NewCredential(unsignedJWT(t, testNow.Add(lifetime))),
		RefreshToken: work.NewCredential("rotated-refresh"),
		IDToken:      work.NewCredential("rotated-id"),
	}
	h.refresher.script(reply{res: res, outcome: RefreshRotated})
	return res
}

func (h *harness) scripts(replies ...reply) { h.refresher.script(replies...) }

// storedAccess returns the access token currently in the store.
func (h *harness) storedAccess(t *testing.T) string {
	t.Helper()
	file, err := parseCredentialFile(h.store.Read(CredentialKey))
	if err != nil {
		t.Fatalf("reading the stored credential: %v", err)
	}
	return file.access.Reveal()
}

// accessTokenIn reads the access token out of a document handed to a sandbox.
//
// The seam yields an opaque file, so a test that wants to know WHICH token was
// handed over has to parse it. That asymmetry is deliberate and sits here on
// purpose: this package knows the format, so its tests may assert on it.
func accessTokenIn(t *testing.T, file work.CredentialFile) string {
	t.Helper()
	var doc struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(file.Reveal(), &doc); err != nil {
		t.Fatalf("the document handed to a sandbox is not JSON: %v", err)
	}
	return doc.Tokens.AccessToken
}

// --- the common path ---------------------------------------------------------

func TestSourceReturnsTheStoredTokenWithoutRefreshingWhenItIsFarFromExpiry(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 72*time.Hour, refreshState{})

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	if accessTokenIn(t, got) != h.storedAccess(t) {
		t.Error("ManagedCredentialFile returned a token other than the stored one")
	}
	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("presented the refresh token %d times for a token days from expiry, want 0", calls)
	}
	if _, puts := h.store.Counts(); puts != 0 {
		t.Errorf("wrote to the secret %d times on the read-only path, want 0", puts)
	}
}

func TestSourceNeverYieldsTheRefreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 72*time.Hour, refreshState{})

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	// Over the whole document, not the field we modelled: the token must not
	// surface anywhere, including in a key a future codex release adds.
	if strings.Contains(string(got.Reveal()), "stored-refresh") {
		t.Fatal("ManagedCredentialFile yielded the refresh token; a sandbox handed this could rotate the credential out from under the worker")
	}
}

// The rotation path is the dangerous one, and it is the one every other leak
// assertion here misses: they run on the fast path, where no refresh fires and
// the token being kept out of the document is the one already in the store.
// After a rotation the token in question is live and was minted by this very
// call. `usable` derives the sandbox copy with `rotated.accessOnly()`; the
// mistake one keystroke away is `rotated.encode()`, and a sandbox handed that
// document can rotate the credential out from under the worker, which then
// reads a spent token and trips ErrSingleWriterViolated hours later with no
// proximate cause.
func TestSourceNeverYieldsTheRotatedRefreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{})
	res := h.rotatesTo(t, 72*time.Hour)

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	// Without this the test would pass on the fast path and assert nothing
	// about rotation at all.
	if calls := h.refresher.Calls(); calls != 1 {
		t.Fatalf("presented the refresh token %d times, want exactly 1; no rotation happened and this test asserts nothing", calls)
	}

	doc := string(got.Reveal())
	if strings.Contains(doc, res.RefreshToken.Reveal()) {
		t.Error("the ROTATED refresh token reached the sandbox document; it is live, and a sandbox holding it can rotate the credential out from under the worker")
	}
	if strings.Contains(doc, "stored-refresh") {
		t.Error("the pre-rotation refresh token reached the sandbox document")
	}
	// Blanked, not dropped: an absent or null key fails to deserialize in
	// codex-cli, so the sandbox would not start at all.
	if !strings.Contains(doc, `"`+keyRefreshToken+`":""`) {
		t.Errorf("the rotated sandbox document carries no blanked %s: %s", keyRefreshToken, doc)
	}
}

func TestSourceRefreshesWhenTheTokenExpiresInsideTheRefreshMargin(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 7, LastWriter: "someone-earlier"})
	res := h.rotatesTo(t, 72*time.Hour)

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	if accessTokenIn(t, got) != res.AccessToken.Reveal() {
		t.Error("ManagedCredentialFile returned a token other than the rotated one")
	}
	if calls := h.refresher.Calls(); calls != 1 {
		t.Errorf("presented the refresh token %d times, want exactly 1", calls)
	}
	if h.storedAccess(t) != res.AccessToken.Reveal() {
		t.Error("the rotated access token was not stored")
	}

	state := storedState(t, h.store)
	if state.Serial != 8 {
		t.Errorf("serial = %d after one rotation, want 8", state.Serial)
	}
	if state.LastWriter != testHolder {
		t.Errorf("last_writer = %q, want the holder that performed the rotation", state.LastWriter)
	}
	if state.Attempt != nil {
		t.Errorf("attempt = %+v after a settled rotation, want it cleared", state.Attempt)
	}
}

func TestSourceStampsTheRefreshTimeFromTheInjectedClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{})
	h.rotatesTo(t, 72*time.Hour)

	if _, err := h.source.ManagedCredentialFile(context.Background()); err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	if got := string(h.store.Read(CredentialKey)); !strings.Contains(got, `"last_refresh":"2026-07-28T00:00:00Z"`) {
		t.Errorf("the stored file carries no last_refresh from the injected clock")
	}
}

// --- the lease ---------------------------------------------------------------

func TestSourceTakesTheLeaseBeforeItPresentsTheRefreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{})
	h.rotatesTo(t, 72*time.Hour)

	var atPresentation [][]string
	h.refresher.storeState = func(int) { atPresentation = h.store.WrittenKeys() }

	if _, err := h.source.ManagedCredentialFile(context.Background()); err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}

	// This is the whole mechanism in one assertion. The lease is a lease only
	// because it is written BEFORE the token is presented; written after, it
	// is an audit log of a race that already happened.
	if len(atPresentation) != 1 {
		t.Fatalf("writes before the presentation = %v, want exactly one — the lease", atPresentation)
	}
	if len(atPresentation[0]) != 1 || atPresentation[0][0] != StateKey {
		t.Errorf("the write before the presentation carried %v, want only %s", atPresentation[0], StateKey)
	}
}

func TestSourceLetsOnlyOneOfTwoIndependentSourcesPresentTheToken(t *testing.T) {
	t.Parallel()
	// Two Source values over one store is the only shape that models two
	// processes. An in-process gate cannot make this pass, so deleting the
	// lease CAS fails it — which is what makes it evidence rather than
	// decoration.
	store := storefake.New(map[string][]byte{
		CredentialKey: seedFile(t, unsignedJWT(t, testNow.Add(30*time.Minute)), "stored-refresh"),
	})

	var (
		entered    sync.WaitGroup
		leaseLost  = make(chan struct{})
		settled    = make(chan struct{})
		lostOnce   sync.Once
		settleOnce sync.Once
	)
	entered.Add(2)

	// Both readers must leave Get holding the same version, or there is no
	// contention to exclude. Holding them after the read rather than before it
	// is what makes that true: a reader held before the read can be overtaken
	// by the other's lease write and observe a version that already carries
	// it. The compare-and-swap itself is left to race exactly as it would in
	// production.
	store.AfterGet = func(n int) {
		if n <= 2 {
			entered.Done()
			entered.Wait()
		}
	}
	store.BeforeGet = func(n int) {
		if n <= 2 {
			return
		}
		// A later read belongs to the loser re-reading. Hold it until the
		// winner's rotation is durable, so what it finds is a fact rather than
		// a scheduling accident.
		select {
		case <-settled:
		case <-time.After(5 * time.Second):
			t.Error("the winner never settled its rotation")
		}
	}
	store.AfterPut = func(_ int, keys []string, err error) {
		if errors.Is(err, work.ErrVersionConflict) {
			lostOnce.Do(func() { close(leaseLost) })
		}
		if err == nil {
			for _, k := range keys {
				if k == CredentialKey {
					settleOnce.Do(func() { close(settled) })
				}
			}
		}
	}

	a := newHarnessOver(t, store)
	b := newHarnessOver(t, store)
	b.source.holder = "worker-other/3c4d"
	rotated := a.rotatesTo(t, 72*time.Hour)
	b.refresher.script(reply{res: rotated, outcome: RefreshRotated})

	// Hold the winner inside the presentation until the loser has been
	// excluded. If the lease is removed, nobody is ever excluded and this
	// fails loudly instead of hanging.
	hold := func(int) {
		select {
		case <-leaseLost:
		case <-time.After(5 * time.Second):
			t.Error("no actor was excluded from presenting: the lease is not doing its job, and both actors are about to spend the same single-use refresh token")
		}
	}
	a.refresher.gate = hold
	b.refresher.gate = hold

	var (
		wg   sync.WaitGroup
		got  [2]work.CredentialFile
		errs [2]error
	)
	wg.Add(2)
	for i, src := range []*Source{a.source, b.source} {
		go func() {
			defer wg.Done()
			got[i], errs[i] = src.ManagedCredentialFile(context.Background())
		}()
	}
	wg.Wait()

	presented := a.refresher.Calls() + b.refresher.Calls()
	if presented != 1 {
		t.Fatalf("the refresh token was presented %d times by two actors, want exactly 1 — a second presentation invalidates the credential for the whole system", presented)
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if accessTokenIn(t, got[i]) != rotated.AccessToken.Reveal() {
			t.Errorf("caller %d received a token other than the single rotation's", i)
		}
	}
}

func TestSourceWaitsForALiveForeignLeaseRatherThanPresenting(t *testing.T) {
	t.Parallel()
	foreign := &attempt{
		Holder:         "worker-elsewhere/9c0a",
		StartedAt:      testNow,
		LeaseExpiresAt: testNow.Add(5 * time.Minute),
		Serial:         3,
	}
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 3, Attempt: foreign})

	// The foreign holder settles between our first and second read. We must
	// find its rotation and use it, never present a token it has already spent.
	rotated := unsignedJWT(t, testNow.Add(72*time.Hour))
	h.store.BeforeGet = func(n int) {
		if n != 2 {
			return
		}
		settled, err := encodeRefreshState(refreshState{Serial: 4, LastWriter: foreign.Holder})
		if err != nil {
			t.Errorf("encoding the foreign holder's settle: %v", err)
			return
		}
		h.store.ForceWrite(map[string][]byte{
			CredentialKey: seedFile(t, rotated, "rotated-refresh"),
			StateKey:      settled,
		})
	}

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	if accessTokenIn(t, got) != rotated {
		t.Error("ManagedCredentialFile did not return the token the lease holder stored")
	}
	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("presented the refresh token %d times while another holder held the lease, want 0", calls)
	}
	if slept := h.clock.Slept(); len(slept) != 1 {
		t.Errorf("waited %v, want exactly one poll interval on the injected clock", slept)
	}
}

func TestSourceReportsARefreshInProgressAsRetryableWhenTheForeignLeaseOutlivesTheWait(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 3, Attempt: &attempt{
		Holder:         "worker-elsewhere/9c0a",
		StartedAt:      testNow,
		LeaseExpiresAt: testNow.Add(365 * 24 * time.Hour),
		Serial:         3,
	}})

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrRefreshInProgress) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrRefreshInProgress", err)
	}
	// Nothing was presented, so this is the one contended case a retry fixes.
	if errors.Is(err, work.ErrPermanent) {
		t.Error("waiting behind another holder's lease must stay retryable — nothing was presented")
	}
	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("presented the refresh token %d times, want 0", calls)
	}
}

func TestSourceHonoursABlockedCallersOwnCancellation(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{})
	h.rotatesTo(t, 72*time.Hour)

	held := make(chan struct{})
	release := make(chan struct{})
	h.refresher.gate = func(int) {
		close(held)
		<-release
	}
	defer close(release)

	go func() { _, _ = h.source.ManagedCredentialFile(context.Background()) }()
	<-held

	// Caller A's deadline is not caller B's. A mutex has no context-aware
	// acquire, so this is the assertion that a mutex implementation fails.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.source.ManagedCredentialFile(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a blocked caller returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled caller stayed blocked behind another caller's refresh; graceful drain cannot work")
	}
	if calls := h.refresher.Calls(); calls != 1 {
		t.Errorf("presented the refresh token %d times, want 1 — the cancelled caller must not present", calls)
	}
}

func TestSourceDoesNotRefreshAtAllWhenManyCallersShareAFreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 72*time.Hour, refreshState{})

	var wg sync.WaitGroup
	wg.Add(64)
	for range 64 {
		go func() {
			defer wg.Done()
			if _, err := h.source.ManagedCredentialFile(context.Background()); err != nil {
				t.Errorf("ManagedCredentialFile: %v", err)
			}
		}()
	}
	wg.Wait()

	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("presented the refresh token %d times for a fresh token, want 0", calls)
	}
	if _, puts := h.store.Counts(); puts != 0 {
		t.Errorf("wrote to the secret %d times, want 0", puts)
	}
}

func TestSourceSerialisesTwoSuccessiveExpiriesIntoTwoRefreshes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{})
	// The second rotation must be scripted too, or a Source that cached the
	// first forever would pass the single-refresh assertions by accident.
	h.scripts(
		reply{res: Refreshed{
			AccessToken:  work.NewCredential(unsignedJWT(t, testNow.Add(100*time.Hour))),
			RefreshToken: work.NewCredential("rotated-refresh-1"),
		}, outcome: RefreshRotated},
		reply{res: Refreshed{
			AccessToken:  work.NewCredential(unsignedJWT(t, testNow.Add(200*time.Hour))),
			RefreshToken: work.NewCredential("rotated-refresh-2"),
		}, outcome: RefreshRotated},
	)

	race := func() {
		var wg sync.WaitGroup
		wg.Add(8)
		for range 8 {
			go func() {
				defer wg.Done()
				if _, err := h.source.ManagedCredentialFile(context.Background()); err != nil {
					t.Errorf("ManagedCredentialFile: %v", err)
				}
			}()
		}
		wg.Wait()
	}
	race()
	h.clock.Advance(99 * time.Hour)
	race()

	if calls := h.refresher.Calls(); calls != 2 {
		t.Errorf("presented the refresh token %d times across two expiries, want exactly 2", calls)
	}
	if state := storedState(t, h.store); state.Serial != 2 {
		t.Errorf("serial = %d, want 2 — one increment per rotation", state.Serial)
	}
}

// --- outcome classification and the attempt marker ---------------------------

func TestSourceReleasesTheLeaseAndRetriesWhenTheRequestNeverReachedTheWire(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.scripts(reply{outcome: RefreshNotSent, err: errors.New("dial tcp: connection refused")})

	_, err := h.source.ManagedCredentialFile(context.Background())
	if err == nil {
		t.Fatal("ManagedCredentialFile succeeded after a failed presentation")
	}
	// A DNS failure or a refused connection must stay an ordinary blip. If it
	// halted, every network hiccup would demand a browser login.
	if errors.Is(err, work.ErrPermanent) {
		t.Error("a request that never reached the wire must stay retryable — the token was not presented")
	}
	if state := storedState(t, h.store); state.Attempt != nil {
		t.Errorf("attempt = %+v, want the lease released so the next caller may present", state.Attempt)
	}
	if got := h.storedAccess(t); got == "" || got == "rotated" {
		t.Error("the stored credential was disturbed by a failed presentation")
	}
}

func TestSourceHaltsAndLeavesTheAttemptUnresolvedWhenTheOutcomeIsUnknown(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.scripts(reply{outcome: RefreshUnknown, err: errors.New("context deadline exceeded")})

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrRefreshOutcomeUnknown) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrRefreshOutcomeUnknown", err)
	}
	if !errors.Is(err, work.ErrPermanent) {
		t.Error("an unknown outcome must be permanent: retrying presents a token that may already be spent")
	}
	// The marker left behind is the whole point. It is what stops the next
	// caller, and the next process, from presenting the same token again.
	state := storedState(t, h.store)
	if state.Attempt == nil || state.Attempt.Holder != testHolder || state.Attempt.Serial != 4 {
		t.Fatalf("attempt = %+v, want our unresolved attempt left in place", state.Attempt)
	}
	if state.Attempt.Outcome != "" {
		t.Errorf("outcome = %q, want it absent — the outcome is precisely what we do not know", state.Attempt.Outcome)
	}
	_, _, deaths := h.metrics.snapshot()
	if len(deaths) != 1 || deaths[0] != DeathOutcomeUnknown {
		t.Errorf("recorded deaths = %v, want one %s", deaths, DeathOutcomeUnknown)
	}
}

func TestSourceRefusesToPresentATokenWhoseUnresolvedAttemptHasAlreadyBeenTakenOver(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4, Attempt: &attempt{
		Holder:         "worker-second/1111",
		StartedAt:      testNow.Add(-time.Hour),
		LeaseExpiresAt: testNow.Add(-55 * time.Minute),
		Serial:         4,
		TakeoverOf:     "worker-first/0000",
	}})

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrRefreshOutcomeUnknown) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrRefreshOutcomeUnknown", err)
	}
	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("presented %d times, want 0 — one takeover per generation is the bound, and it is used", calls)
	}
}

func TestSourceTakesOverExactlyOneExpiredAttemptAndRecordsThePreviousHolder(t *testing.T) {
	t.Parallel()
	const dead = "worker-dead/0000"
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4, Attempt: &attempt{
		Holder:         dead,
		StartedAt:      testNow.Add(-time.Hour),
		LeaseExpiresAt: testNow.Add(-55 * time.Minute),
		Serial:         4,
	}})
	// The holder's lease write is the last thing it did; scripting a rejection
	// here would confuse the takeover with its outcome, so it rotates.
	res := h.rotatesTo(t, 72*time.Hour)

	var takeoverAtPresentation string
	h.refresher.storeState = func(int) { takeoverAtPresentation = takeoverOf(t, h.store) }

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	if accessTokenIn(t, got) != res.AccessToken.Reveal() {
		t.Error("the takeover did not return the rotated token")
	}
	if takeoverAtPresentation != dead {
		t.Errorf("takeover_of = %q at the moment of presentation, want the dead holder recorded before we present", takeoverAtPresentation)
	}
	if _, takeovers, _ := h.metrics.snapshot(); takeovers != 1 {
		t.Errorf("recorded %d takeovers, want 1 — a holder died mid-refresh and that must be visible", takeovers)
	}
	if !strings.Contains(h.logs.String(), dead) {
		t.Error("the takeover was not logged with the holder it seized from")
	}
}

func takeoverOf(t *testing.T, store *storefake.Store) string {
	t.Helper()
	state, err := parseRefreshState(store.Read(StateKey))
	if err != nil || state.Attempt == nil {
		return ""
	}
	return state.Attempt.TakeoverOf
}

func TestSourceHaltsNonRetryablyWhenTheProviderRejectsTheRefreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.scripts(reply{outcome: RefreshRejected, err: errors.New("invalid_grant")})
	stored := h.storedAccess(t)

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrRefreshRejected", err)
	}
	if !errors.Is(err, work.ErrPermanent) {
		t.Error("a rejected refresh token must be permanent — asking again cannot un-spend it")
	}
	if h.storedAccess(t) != stored {
		t.Error("a rejection must not disturb the stored credential")
	}
	state := storedState(t, h.store)
	if state.Attempt == nil || state.Attempt.Outcome != outcomeRejected {
		t.Fatalf("attempt = %+v, want the rejection recorded so the next caller need not learn it again", state.Attempt)
	}
}

func TestSourceReportsAPreviouslyRecordedRejectionWithoutPresentingAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4, Attempt: &attempt{
		Holder:         "worker-earlier/0000",
		StartedAt:      testNow.Add(-time.Hour),
		LeaseExpiresAt: testNow.Add(-55 * time.Minute),
		Serial:         4,
		Outcome:        outcomeRejected,
	}})

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrRefreshRejected", err)
	}
	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("presented %d times against a token already known dead, want 0", calls)
	}
}

func TestSourceDoesNotBeginAPresentationOnceTheContextIsCancelled(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.rotatesTo(t, 72*time.Hour)

	// A worker draining on SIGTERM must not start something it cannot finish.
	// Cancelling as the lease lands is the exact window that matters.
	ctx, cancel := context.WithCancel(context.Background())
	h.store.AfterPut = func(_ int, _ []string, _ error) { cancel() }

	_, err := h.source.ManagedCredentialFile(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ManagedCredentialFile returned %v, want the context error", err)
	}
	if errors.Is(err, work.ErrPermanent) {
		t.Error("a cancellation before presenting is retryable — nothing was spent")
	}
	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("presented %d times after cancellation, want 0", calls)
	}
}

// --- settle and durability ---------------------------------------------------

func TestSourceSucceedsWhenItsOwnWriteLandedButTheResponseWasLost(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	res := h.rotatesTo(t, 72*time.Hour)

	// The settle is the second write; the lease is the first. It applies and
	// then reports failure, which is what a timed-out but landed update looks
	// like — and is the most likely transient failure in the system.
	h.store.ApplyThenFail = func(n int) error {
		if n == 2 {
			return errors.New("context deadline exceeded")
		}
		return nil
	}

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile returned %v; a writer that cannot recognise its own landed write turns the commonest blip into a dead credential", err)
	}
	if accessTokenIn(t, got) != res.AccessToken.Reveal() {
		t.Error("ManagedCredentialFile returned a token other than the rotated one")
	}
	if calls := h.refresher.Calls(); calls != 1 {
		t.Errorf("presented %d times, want 1 — never re-present to recover a lost response", calls)
	}
}

func TestSourceRetriesTheSettleWhenOnlyUnrelatedMetadataChanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	res := h.rotatesTo(t, 72*time.Hour)

	// A controller touching a label moves the resourceVersion without touching
	// our lease. That is contention to retry, not an invariant violation.
	h.store.BeforePut = func(n int) {
		if n == 2 {
			h.store.ForceWrite(map[string][]byte{"unrelated-annotation": []byte("x")})
		}
	}

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	if accessTokenIn(t, got) != res.AccessToken.Reveal() {
		t.Error("ManagedCredentialFile returned a token other than the rotated one")
	}
	if h.storedAccess(t) != res.AccessToken.Reveal() {
		t.Error("the rotated token was not stored after the retry")
	}
	if calls := h.refresher.Calls(); calls != 1 {
		t.Errorf("presented %d times, want 1", calls)
	}
}

func TestSourceHaltsWhenAForeignWriterChangedTheCredentialGeneration(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.rotatesTo(t, 72*time.Hour)

	h.store.BeforePut = func(n int) {
		if n != 2 {
			return
		}
		foreign, err := encodeRefreshState(refreshState{Serial: 9, LastWriter: "somebody-else/ffff"})
		if err != nil {
			t.Errorf("encoding the foreign writer's state: %v", err)
			return
		}
		h.store.ForceWrite(map[string][]byte{StateKey: foreign})
	}

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrSingleWriterViolated) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrSingleWriterViolated", err)
	}
	if !errors.Is(err, work.ErrPermanent) {
		t.Error("a second writer must halt permanently — retrying races it again")
	}
	if calls := h.refresher.Calls(); calls != 1 {
		t.Errorf("presented %d times, want 1 — a conflict after presenting is never a reason to present again", calls)
	}
	if !strings.Contains(h.logs.String(), "somebody-else/ffff") {
		t.Error("the foreign writer was not named in the logs; investigating who wrote is the first recovery step")
	}
}

func TestSourceRetriesATransientSettleFailureRatherThanLosingARotatedToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	res := h.rotatesTo(t, 72*time.Hour)

	h.store.PutErr = func(n int) error {
		if n == 2 || n == 3 {
			return errors.New("etcdserver: request timed out")
		}
		return nil
	}

	got, err := h.source.ManagedCredentialFile(context.Background())
	if err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	if accessTokenIn(t, got) != res.AccessToken.Reveal() {
		t.Error("ManagedCredentialFile returned a token other than the rotated one")
	}
	if h.storedAccess(t) != res.AccessToken.Reveal() {
		t.Error("the rotated token was not stored")
	}
	// The backoff is visible without waiting for it, which is the whole reason
	// the clock is injected.
	if slept := h.clock.Slept(); len(slept) != 2 || slept[1] <= slept[0] {
		t.Errorf("backoff = %v, want two increasing waits on the injected clock", slept)
	}
}

func TestSourceReportsTheCredentialLostWhenARotatedTokenCannotBeStored(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.rotatesTo(t, 72*time.Hour)
	h.store.PutErr = func(n int) error {
		if n >= 2 {
			return errors.New("etcdserver: request timed out")
		}
		return nil
	}

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrCredentialLost) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrCredentialLost", err)
	}
	// Returning the in-hand token here would work perfectly, unattended, for
	// days — and then brick with nobody watching and no proximate cause.
	if !errors.Is(err, work.ErrPermanent) {
		t.Error("a rotation that could not be stored must halt: the stored refresh token is already dead")
	}
	if _, _, deaths := h.metrics.snapshot(); len(deaths) != 1 || deaths[0] != DeathCredentialLost {
		t.Errorf("recorded deaths = %v, want one %s", deaths, DeathCredentialLost)
	}
}

func TestSourceStoresTheRotatedTokenBeforeFailingOnAShortLivedRefreshResult(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	// A provider behaviour change, not a bug of ours. Dropping the pair would
	// spend a single-use refresh token for nothing.
	res := h.rotatesTo(t, 20*time.Minute)

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrRefreshTooShortLived) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrRefreshTooShortLived", err)
	}
	if h.storedAccess(t) != res.AccessToken.Reveal() {
		t.Fatal("the rotated pair was not stored before failing; the old refresh token is spent and the new one is gone")
	}
	if state := storedState(t, h.store); state.Serial != 5 {
		t.Errorf("serial = %d, want the rotation counted", state.Serial)
	}
}

// --- unseeded, construction, Validate ----------------------------------------

func TestSourceReportsTheCredentialUnseededWhenTheSecretIsAbsent(t *testing.T) {
	t.Parallel()
	h := newHarnessOver(t, storefake.New(nil))
	h.store.BeforeGet = func(int) {}
	h.store.PutErr = nil

	// The store reports absence with the domain sentinel; the Source turns
	// that into the one message whose remedy is a human with a browser.
	missing := &notFoundStore{}
	source, err := New(missing, h.refresher, h.clock, slog.New(slog.NewJSONHandler(h.logs, nil)), testHolder, testStageDuration)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrUnseeded) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrUnseeded", err)
	}
	if !errors.Is(err, work.ErrPermanent) {
		t.Error("an unseeded credential must be permanent — no retry seeds it")
	}
	for _, want := range []string{CredentialKey, "codex login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the unseeded error does not mention %q; it is the whole recovery path", want)
		}
	}
}

type notFoundStore struct{}

func (notFoundStore) Get(context.Context) (map[string][]byte, work.SecretVersion, error) {
	return nil, work.SecretVersion{}, work.ErrSecretNotFound
}

func (notFoundStore) Put(context.Context, map[string][]byte, work.SecretVersion) (work.SecretVersion, error) {
	return work.SecretVersion{}, errors.New("the credential secret does not exist")
}

func TestSourceTreatsAnUnreadableSecretAsRetryable(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 72*time.Hour, refreshState{})
	h.store.BeforeGet = func(int) {}

	source, err := New(unreadableStore{}, h.refresher, h.clock, slog.New(slog.NewJSONHandler(h.logs, nil)), testHolder, testStageDuration)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = source.ManagedCredentialFile(context.Background())
	if err == nil {
		t.Fatal("ManagedCredentialFile succeeded against an unreadable secret")
	}
	// "I could not tell" is not "it is not there". Collapsing the two would
	// turn an apiserver blip into a demand for a browser login.
	if errors.Is(err, work.ErrPermanent) {
		t.Error("an unreadable secret must stay retryable")
	}
}

type unreadableStore struct{ notFoundStore }

func (unreadableStore) Get(context.Context) (map[string][]byte, work.SecretVersion, error) {
	return nil, work.SecretVersion{}, errors.New("etcdserver: request timed out")
}

func TestSourceValidatesTheStoredCredentialWithoutPresentingOrWritingIt(t *testing.T) {
	t.Parallel()
	// Boot is the first moment of a Recreate rollout — the one window in which
	// a terminating pod may still hold the lease. A boot check that could
	// refresh would schedule a presentation into the least safe moment in the
	// service's life.
	h := newHarness(t, 72*time.Hour, refreshState{Serial: 2})

	if err := h.source.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if calls := h.refresher.Calls(); calls != 0 {
		t.Errorf("Validate presented the refresh token %d times, want 0", calls)
	}
	if _, puts := h.store.Counts(); puts != 0 {
		t.Errorf("Validate wrote %d times, want 0", puts)
	}
}

func TestSourceValidateRefusesACredentialThatIsNotSeeded(t *testing.T) {
	t.Parallel()
	h := newHarnessOver(t, storefake.New(map[string][]byte{CredentialKey: []byte(`{"tokens":{}}`)}))

	if err := h.source.Validate(context.Background()); !errors.Is(err, ErrUnseeded) {
		t.Fatalf("Validate returned %v, want ErrUnseeded", err)
	}
}

func TestSourceValidateWarnsAboutAnUnresolvedAttemptWithoutFailingBoot(t *testing.T) {
	t.Parallel()
	const dead = "worker-dead/0000"
	h := newHarness(t, 72*time.Hour, refreshState{Serial: 4, Attempt: &attempt{
		Holder:         dead,
		StartedAt:      testNow.Add(-time.Hour),
		LeaseExpiresAt: testNow.Add(-55 * time.Minute),
		Serial:         4,
	}})

	// The access token is fine for days; the refresh token behind it may be
	// spent. Without this the discovery happens days later with no context.
	if err := h.source.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !strings.Contains(h.logs.String(), dead) {
		t.Error("boot did not name the holder of the unresolved attempt")
	}
	if _, _, deaths := h.metrics.snapshot(); len(deaths) != 1 || deaths[0] != DeathOutcomeUnknown {
		t.Errorf("recorded deaths = %v, want one %s at boot", deaths, DeathOutcomeUnknown)
	}
}

func TestSourceValidateWarnsAboutAnAlreadyRefusedCredential(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 72*time.Hour, refreshState{Serial: 4, Attempt: &attempt{
		Holder:         "worker-earlier/0000",
		StartedAt:      testNow.Add(-time.Hour),
		LeaseExpiresAt: testNow.Add(-55 * time.Minute),
		Serial:         4,
		Outcome:        outcomeRejected,
	}})

	// The stored access token is the last one this credential will ever have.
	// Boot must not fail — the worker still works — but it must say so, or the
	// first anyone hears of it is when a stage dies days later.
	if err := h.source.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, _, deaths := h.metrics.snapshot(); len(deaths) != 1 || deaths[0] != DeathRejected {
		t.Errorf("recorded deaths = %v, want one %s", deaths, DeathRejected)
	}
}

func TestNewRefusesAnIncompleteSource(t *testing.T) {
	t.Parallel()
	var (
		store     = storefake.New(nil)
		refresher = &fakeRefresher{}
		clk       = clocktest.NewFake(testNow)
		log       = slog.New(slog.NewJSONHandler(&strings.Builder{}, nil))
	)
	cases := map[string]func() (*Source, error){
		"no store":     func() (*Source, error) { return New(nil, refresher, clk, log, testHolder, testStageDuration) },
		"no refresher": func() (*Source, error) { return New(store, nil, clk, log, testHolder, testStageDuration) },
		"no clock":     func() (*Source, error) { return New(store, refresher, nil, log, testHolder, testStageDuration) },
		"no logger":    func() (*Source, error) { return New(store, refresher, clk, nil, testHolder, testStageDuration) },
		// A lease with an unattributable holder cannot be investigated at 3am,
		// which is the only moment anyone reads it.
		"no holder": func() (*Source, error) { return New(store, refresher, clk, log, "", testStageDuration) },
		"a margin that is not positive": func() (*Source, error) {
			return New(store, refresher, clk, log, testHolder, testStageDuration, WithRefreshMargin(0))
		},
		"a lease TTL shorter than the presentation it bounds": func() (*Source, error) {
			return New(store, refresher, clk, log, testHolder, testStageDuration, WithLeaseTTL(time.Second), WithRefreshTimeout(time.Minute))
		},
	}
	for name, construct := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source, err := construct()
			if err == nil {
				t.Fatal("New returned a usable-but-invalid Source, want an error")
			}
			if source != nil {
				t.Error("New returned both a Source and an error")
			}
		})
	}
}

func TestNewDefaultsTheTuningItsSafetyArgumentDependsOn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 72*time.Hour, refreshState{})
	s := h.source

	for _, c := range []struct {
		name string
		got  any
		want any
	}{
		{"refresh margin", s.margin, defaultRefreshMargin},
		{"refresh timeout", s.refreshTimeout, defaultRefreshTimeout},
		{"lease TTL", s.leaseTTL, defaultLeaseTTL},
		{"lease poll interval", s.leasePoll, defaultLeasePoll},
		{"wait rounds", s.waitRounds, defaultWaitRounds},
		{"store attempts", s.storeAttempts, defaultStoreAttempts},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// The takeover argument rests on a lease TTL far longer than the bound on
	// one presentation. If that stops holding, the argument stops holding.
	if s.leaseTTL <= s.refreshTimeout {
		t.Error("the lease TTL must outlast the presentation it bounds")
	}
}

// --- review findings -----------------------------------------------------

func TestSourceStoresARotatedRefreshTokenThatArrivedWithoutAnAccessToken(t *testing.T) {
	t.Parallel()
	// A 200 carrying only a rotated refresh token is a successful,
	// non-destructive refresh per the provider's own client. The old token is
	// spent; dropping its replacement is what kills the credential.
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.scripts(reply{res: Refreshed{RefreshToken: work.NewCredential("rotated-refresh")}, outcome: RefreshRotated})

	_, err := h.source.ManagedCredentialFile(context.Background())
	if err == nil {
		t.Fatal("ManagedCredentialFile returned a token when none came back")
	}
	if !errors.Is(err, work.ErrPermanent) {
		t.Error("a rotation with no usable access token must halt rather than loop")
	}

	stored, perr := parseCredentialFile(h.store.Read(CredentialKey))
	if perr != nil {
		t.Fatalf("the stored credential no longer parses: %v", perr)
	}
	if stored.refresh.Reveal() != "rotated-refresh" {
		t.Fatal("the rotated refresh token was not stored; the old one is spent and its replacement is gone")
	}
	if state := storedState(t, h.store); state.Serial != 5 {
		t.Errorf("serial = %d, want the rotation counted", state.Serial)
	}
	if _, _, deaths := h.metrics.snapshot(); len(deaths) != 1 || deaths[0] != DeathNoAccessToken {
		t.Errorf("recorded deaths = %v, want one %s", deaths, DeathNoAccessToken)
	}
}

func TestSourceReportsAReusedRefreshTokenAsASingleWriterViolation(t *testing.T) {
	t.Parallel()
	// "Already presented" is a second holder, not a stale credential. Routing
	// it to "re-seed" hands the second holder a fresh credential to eat too.
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.scripts(reply{outcome: RefreshReused, err: errors.New("refresh_token_reused")})

	_, err := h.source.ManagedCredentialFile(context.Background())
	if !errors.Is(err, ErrSingleWriterViolated) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrSingleWriterViolated", err)
	}
	if _, _, deaths := h.metrics.snapshot(); len(deaths) != 1 || deaths[0] != DeathSingleWriterViolated {
		t.Errorf("recorded deaths = %v, want one %s — the operator must be told to find who, not to re-seed", deaths, DeathSingleWriterViolated)
	}
}

func TestSourcePreservesTheTakeoverBudgetWhenItReleasesWithoutPresenting(t *testing.T) {
	t.Parallel()
	const dead = "worker-dead/0000"
	prior := &attempt{
		Holder:         dead,
		StartedAt:      testNow.Add(-time.Hour),
		LeaseExpiresAt: testNow.Add(-55 * time.Minute),
		Serial:         4,
	}
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4, Attempt: prior})
	h.scripts(reply{outcome: RefreshNotSent, err: errors.New("dial tcp: connection refused")})

	if _, err := h.source.ManagedCredentialFile(context.Background()); err == nil {
		t.Fatal("ManagedCredentialFile succeeded after a failed presentation")
	}

	// Clearing the attempt here would erase the record that the dead holder's
	// outcome is unknown — which is the only thing bounding takeover at one,
	// and the bound the policy was signed off on.
	state := storedState(t, h.store)
	if state.Attempt == nil {
		t.Fatal("the release erased the record that the dead holder's outcome is unknown; takeover is now unbounded")
	}
	if state.Attempt.Holder != dead {
		t.Errorf("attempt holder = %q, want the dead holder %q restored", state.Attempt.Holder, dead)
	}
	if state.Attempt.TakeoverOf != "" {
		t.Errorf("takeover_of = %q, want it clear — this actor never presented, so the one takeover is unspent", state.Attempt.TakeoverOf)
	}
}

func TestSourceRefusesAThirdPresentationAfterATakeoverThatWasUsed(t *testing.T) {
	t.Parallel()
	const dead = "worker-dead/0000"
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4, Attempt: &attempt{
		Holder:         dead,
		StartedAt:      testNow.Add(-time.Hour),
		LeaseExpiresAt: testNow.Add(-55 * time.Minute),
		Serial:         4,
	}})
	// The takeover presents and its outcome is unknown, so the budget is spent.
	h.scripts(reply{outcome: RefreshUnknown, err: errors.New("context deadline exceeded")})

	if _, err := h.source.ManagedCredentialFile(context.Background()); !errors.Is(err, ErrRefreshOutcomeUnknown) {
		t.Fatalf("the takeover returned %v, want ErrRefreshOutcomeUnknown", err)
	}
	presentedByTakeover := h.refresher.Calls()

	// A later actor, at the same generation, must not present again: two
	// unknown outcomes in a row is exactly the crash-loop the bound prevents.
	h.clock.Advance(2 * time.Hour)
	if _, err := h.source.ManagedCredentialFile(context.Background()); !errors.Is(err, ErrRefreshOutcomeUnknown) {
		t.Fatalf("the follow-up returned %v, want ErrRefreshOutcomeUnknown", err)
	}
	if h.refresher.Calls() != presentedByTakeover {
		t.Fatalf("presented %d times total, want %d — the one takeover was already spent",
			h.refresher.Calls(), presentedByTakeover)
	}
}

func TestSourceReportsBeingTakenOverAsALostCredentialRatherThanAForeignWriter(t *testing.T) {
	t.Parallel()
	// Our settle failed and another holder took our expired lease over. That
	// is not a foreign writer, and sending an operator hunting one wastes the
	// only person who can fix it.
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.rotatesTo(t, 72*time.Hour)
	h.store.PutErr = func(n int) error {
		if n >= 2 {
			return errors.New("etcdserver: request timed out")
		}
		return nil
	}
	h.store.BeforePut = func(n int) {
		if n != 3 {
			return
		}
		taken, err := encodeRefreshState(refreshState{Serial: 4, Attempt: &attempt{
			Holder: "worker-later/9999", StartedAt: testNow, LeaseExpiresAt: testNow.Add(5 * time.Minute),
			Serial: 4, TakeoverOf: testHolder,
		}})
		if err != nil {
			t.Errorf("encoding the taker-over's state: %v", err)
			return
		}
		h.store.ForceWrite(map[string][]byte{StateKey: taken})
	}

	_, err := h.source.ManagedCredentialFile(context.Background())
	if errors.Is(err, ErrSingleWriterViolated) {
		t.Fatal("a holder taking over our own expired lease was reported as a foreign writer")
	}
	if !errors.Is(err, ErrCredentialLost) {
		t.Fatalf("ManagedCredentialFile returned %v, want ErrCredentialLost", err)
	}
	if !strings.Contains(h.logs.String(), "worker-later/9999") {
		t.Error("the holder that took over was not named")
	}
}

// deadlineStore records whether the writes it served carried a deadline.
type deadlineStore struct {
	*storefake.Store
	mu        sync.Mutex
	deadlines []bool
}

func (d *deadlineStore) Put(ctx context.Context, values map[string][]byte, pre work.SecretVersion) (work.SecretVersion, error) {
	_, ok := ctx.Deadline()
	d.mu.Lock()
	d.deadlines = append(d.deadlines, ok)
	d.mu.Unlock()
	return d.Store.Put(ctx, values, pre)
}

func TestSourceBoundsTheWritesThatFollowAPresentation(t *testing.T) {
	t.Parallel()
	// Nothing bounds lease-write to settle-write, so an Update against a
	// wedged apiserver hangs at TCP level, the lease expires under us, and
	// another holder takes over and presents a token we already spent.
	h := newHarness(t, 30*time.Minute, refreshState{Serial: 4})
	h.rotatesTo(t, 72*time.Hour)
	bounded := &deadlineStore{Store: h.store}
	source, err := New(bounded, h.refresher, h.clock, slog.New(slog.NewJSONHandler(h.logs, nil)), testHolder, testStageDuration)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := source.ManagedCredentialFile(context.Background()); err != nil {
		t.Fatalf("ManagedCredentialFile: %v", err)
	}
	bounded.mu.Lock()
	defer bounded.mu.Unlock()
	if len(bounded.deadlines) < 2 {
		t.Fatalf("saw %d writes, want at least the lease and the settle", len(bounded.deadlines))
	}
	if !bounded.deadlines[len(bounded.deadlines)-1] {
		t.Error("the settle write carried no deadline; a wedged apiserver hangs it past the lease it holds")
	}
}

func TestNewRefusesARefreshMarginAStageCanOutlive(t *testing.T) {
	t.Parallel()
	// A Run Worker cannot refresh the projected credential itself, so a token
	// that reaches the provider client's own
	// 5-minute window mid-stage fails the stage AND hammers the provider's
	// auth endpoint — verified live: exit 1 after 104 requests in 35s.
	_, err := New(newFakeStore(t), &fakeRefresher{}, clocktest.NewFake(testNow),
		slog.New(slog.NewJSONHandler(&strings.Builder{}, nil)), testHolder, time.Hour,
		WithRefreshMargin(30*time.Minute))
	if err == nil {
		t.Fatal("New accepted a refresh margin shorter than a stage; a Run Worker's token would die mid-stage")
	}
	if _, err := New(newFakeStore(t), &fakeRefresher{}, clocktest.NewFake(testNow),
		slog.New(slog.NewJSONHandler(&strings.Builder{}, nil)), testHolder, time.Hour,
		WithRefreshMargin(time.Hour+clientRefreshWindow+time.Minute)); err != nil {
		t.Fatalf("New refused a margin that does outlast a stage: %v", err)
	}
}
