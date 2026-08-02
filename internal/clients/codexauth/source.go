package codexauth

import (
	"context"
	stderrors "errors"
	"log/slog"
	"time"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/retry"
	"github.com/0x63616c/software-factory/internal/work"
	cberrors "github.com/cockroachdb/errors"
)

// remedy is the one sentence every fatal message ends with, because for all of
// them the next step is the same and it is a human's.
const remedy = "run `codex login` once on an operator workstation and re-seed the secret with scripts/seed-codex-auth.sh; Codex CLI is provisioning-only and never runs in the service"

// Source yields a refresh-token-free credential document to the direct model
// adapter, refreshing and rotating the stored credential when its token nears
// expiry.
//
// It is the only writer of the credential. Exclusion is a lease taken on the
// stored object BEFORE the refresh token is presented, so it holds across
// processes and not merely across goroutines — see the package doc.
type Source struct {
	store     SecretStore
	refresher TokenRefresher
	clock     clock.Clock
	log       *slog.Logger
	holder    string
	metrics   Metrics

	margin         time.Duration
	refreshTimeout time.Duration
	leaseTTL       time.Duration
	leasePoll      time.Duration
	waitRounds     int
	storeAttempts  int
	storeBackoff   time.Duration

	// gate is a capacity-1 channel rather than a mutex because a mutex has no
	// context-aware acquire: a caller blocked behind another's refresh could
	// not honour its own cancellation, and with 60-minute stage contexts it
	// could outlive its own deadline by an hour and defeat graceful drain.
	//
	// It is not the correctness mechanism. It collapses N concurrent callers
	// in one process into one read instead of N reads and N-1 lease conflicts.
	// Remove it and the system is correct and noisy; remove the lease and it
	// is broken.
	gate chan struct{}
}

// clientRefreshWindow is preserved as safety headroom beyond the longest
// model operation. The access-only document cannot refresh itself while a
// request is in flight.
const clientRefreshWindow = 5 * time.Minute

// New constructs a Source. Required dependencies are positional; everything
// tunable is an option with a default that is correct for this service.
//
// holder is positional rather than optional because a lease with a defaulted or
// empty holder identity cannot be attributed at 3am, which is the only hour
// anyone reads one. The composition root passes `<pod name>/<short random>`;
// the suffix distinguishes two runs of the same pod name.
//
// maxStageDuration is the conservative upper bound supplied by the caller for
// one uninterrupted use of the derived credential. It is positional because
// the refresh margin is meaningless without that bound.
func New(store SecretStore, refresher TokenRefresher, clk clock.Clock, log *slog.Logger, holder string, maxStageDuration time.Duration, opts ...Option) (*Source, error) {
	o := options{
		metrics:        noMetrics{},
		margin:         defaultRefreshMargin,
		refreshTimeout: defaultRefreshTimeout,
		leaseTTL:       defaultLeaseTTL,
		leasePoll:      defaultLeasePoll,
		waitRounds:     defaultWaitRounds,
		storeAttempts:  defaultStoreAttempts,
		storeBackoff:   defaultStoreBackoff,
	}
	for _, opt := range opts {
		opt(&o)
	}

	switch {
	case store == nil:
		return nil, cberrors.New("a codex token source needs a secret store")
	case refresher == nil:
		return nil, cberrors.New("a codex token source needs a token refresher")
	case clk == nil:
		return nil, cberrors.New("a codex token source needs a clock")
	case log == nil:
		return nil, cberrors.New("a codex token source needs a logger")
	case holder == "":
		return nil, cberrors.New("a codex token source needs a holder identity: an unattributable lease cannot be investigated")
	case o.metrics == nil:
		return nil, cberrors.New("a codex token source needs a metrics recorder")
	case o.margin <= 0 || o.refreshTimeout <= 0 || o.leaseTTL <= 0 || o.leasePoll <= 0:
		return nil, cberrors.New("a codex token source needs positive durations")
	case o.waitRounds < 1 || o.storeAttempts < 1 || o.storeBackoff <= 0:
		return nil, cberrors.New("a codex token source needs at least one wait round and one store attempt")
	case maxStageDuration <= 0:
		return nil, cberrors.New("a codex token source needs the longest a stage may run, to size the refresh margin against")
	case o.margin <= maxStageDuration+clientRefreshWindow:
		// INV-3. The derived document cannot refresh, so the token it carries
		// must outlive the whole operation plus safety headroom. Checked here
		// for the same reason the
		// lease TTL is: it is a relationship between two tunables, and prose
		// does not survive somebody tuning one of them.
		return nil, cberrors.Newf(
			"the refresh margin (%s) must exceed the longest credential use (%s) plus safety headroom (%s): "+
				"the access-only document cannot refresh, so a shorter margin can expire in flight",
			o.margin, maxStageDuration, clientRefreshWindow)
	case o.leaseTTL <= o.refreshTimeout:
		// The takeover policy rests on the lease outlasting the presentation
		// it bounds. Equal or shorter, an expired lease no longer means "the
		// holder is not coming back".
		return nil, cberrors.Newf("the lease TTL (%s) must outlast the presentation it bounds (%s)", o.leaseTTL, o.refreshTimeout)
	}

	return &Source{
		store: store, refresher: refresher, clock: clk, log: log, holder: holder,
		metrics:        o.metrics,
		margin:         o.margin,
		refreshTimeout: o.refreshTimeout,
		leaseTTL:       o.leaseTTL,
		leasePoll:      o.leasePoll,
		waitRounds:     o.waitRounds,
		storeAttempts:  o.storeAttempts,
		storeBackoff:   o.storeBackoff,
		gate:           make(chan struct{}, 1),
	}, nil
}

// ManagedCredentialFile returns the provider credential document to the
// main-worker adapter: refreshed if it is inside the refresh margin, with the
// refresh token blanked, and good for at least that margin.
//
// It yields the whole file rather than a token because the account id and
// token metadata required by the direct Responses request live in that
// document. Parsing that format stays in one adapter rather than being
// duplicated across model callers.
func (s *Source) ManagedCredentialFile(ctx context.Context) (work.CredentialFile, error) {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return work.CredentialFile{}, cberrors.Wrap(ctx.Err(), "waiting to read the codex credential")
	}

	waitingOn := ""
	for range s.waitRounds {
		result, err := s.round(ctx)
		if result.done || err != nil {
			return result.file, err
		}
		if result.waitingOn != "" {
			waitingOn = result.waitingOn
		}
		if err := s.clock.Sleep(ctx, s.leasePoll); err != nil {
			return work.CredentialFile{}, cberrors.Wrap(err, "waiting for the codex credential to be refreshed")
		}
	}
	if waitingOn == "" {
		waitingOn = "another holder"
	}
	return work.CredentialFile{}, cberrors.Wrapf(ErrRefreshInProgress, "%s held the lease for the whole of our wait", waitingOn)
}

// Validate reports whether a usable credential is stored. It reads and parses
// and does nothing else: it never presents the refresh token and never writes.
//
// Worker boot is the first moment of a Recreate rollout, which is the one
// window in which a terminating pod may still hold the lease, so a boot check
// that could refresh would schedule a presentation into the least safe moment
// in the service's life.
//
// An unresolved attempt is a warning rather than an error. The stored access
// token is good for days while the refresh token behind it may already be
// spent, so failing boot would refuse to start a worker that works — but saying
// nothing would let the discovery happen days later with no context left.
func (s *Source) Validate(ctx context.Context) error {
	values, _, err := s.store.Get(ctx)
	if err != nil {
		return s.readError(err)
	}
	_, state, _, err := s.parse(values)
	if err != nil {
		return err
	}

	att := state.Attempt
	if att == nil || att.Serial != state.Serial {
		return nil
	}
	switch {
	case att.Outcome == outcomeRejected:
		s.metrics.CredentialDead(DeathRejected)
		s.log.WarnContext(ctx, "the provider has already refused this codex refresh token, so the stored access token is the last one",
			"holder", att.Holder, "serial", att.Serial, "remedy", remedy)
	case !att.live(s.clock.Now()):
		s.metrics.CredentialDead(DeathOutcomeUnknown)
		s.log.WarnContext(ctx, "a previous refresh of the codex credential never settled, so its refresh token may already be spent",
			"holder", att.Holder, "started_at", att.StartedAt, "serial", att.Serial, "remedy", remedy)
	}
	return nil
}

// roundResult is one pass of the read-decide-refresh loop. done means the call
// is over — with an answer, or with something a re-read cannot change.
type roundResult struct {
	file      work.CredentialFile
	done      bool
	waitingOn string
}

func (s *Source) round(ctx context.Context) (roundResult, error) {
	values, version, err := s.store.Get(ctx)
	if err != nil {
		return roundResult{done: true}, s.readError(err)
	}
	cred, state, exp, err := s.parse(values)
	if err != nil {
		return roundResult{done: true}, err
	}

	now := s.clock.Now()
	if now.Add(s.margin).Before(exp) {
		// The overwhelmingly common path: no write, no network, no lease.
		file, err := cred.accessOnly()
		if err != nil {
			return roundResult{done: true}, s.unusable(err)
		}
		return roundResult{file: file, done: true}, nil
	}
	return s.refresh(ctx, cred, state, version, now)
}

// parse turns the stored bytes into a credential, its lease state and its
// expiry. Every failure here is unseeded and permanent, and every one of them
// is recorded as a death because the only fix is a human.
func (s *Source) parse(values map[string][]byte) (credentialFile, refreshState, time.Time, error) {
	cred, err := parseCredentialFile(values[CredentialKey])
	if err != nil {
		s.metrics.CredentialDead(DeathUnseeded)
		return credentialFile{}, refreshState{}, time.Time{}, s.unusable(err)
	}
	state, err := parseRefreshState(values[StateKey])
	if err != nil {
		s.metrics.CredentialDead(DeathUnseeded)
		return credentialFile{}, refreshState{}, time.Time{}, s.unusable(err)
	}
	exp, err := expiryOf(cred.access)
	if err != nil {
		s.metrics.CredentialDead(DeathUnseeded)
		return credentialFile{}, refreshState{}, time.Time{}, s.unusable(cberrors.Wrapf(ErrUnseeded, "%v", err))
	}
	return cred, state, exp, nil
}

// readError distinguishes a secret that is not there from one we could not
// read. Collapsing the two would turn an apiserver blip into a demand for a
// browser login, and a genuinely absent secret into an endless retry.
func (s *Source) readError(err error) error {
	if stderrors.Is(err, work.ErrSecretNotFound) {
		s.metrics.CredentialDead(DeathUnseeded)
		return s.unusable(cberrors.Wrapf(ErrUnseeded, "the secret holding %s does not exist", CredentialKey))
	}
	return cberrors.Wrap(err, "reading the codex credential secret")
}

// unusable appends the one remedy every fatal condition here shares.
func (s *Source) unusable(err error) error {
	return cberrors.WithMessage(err, remedy)
}

// refresh takes the lease, presents the token, and settles the result.
//
// The ordering is the mechanism. The compare-and-swap that takes the lease
// happens BEFORE the token is presented, so of N actors holding one version
// exactly one reaches the provider — cross-process, cross-node, including a
// terminating pod during a rollout. A conflict here is contention and nothing
// destructive has happened, so it re-reads; a conflict AFTER presenting is news
// and never re-presents.
func (s *Source) refresh(ctx context.Context, cred credentialFile, state refreshState, version work.SecretVersion, now time.Time) (roundResult, error) {
	takeoverOf := ""
	if att := state.Attempt; att != nil && att.Serial == state.Serial {
		switch {
		case att.Outcome == outcomeRejected:
			// Somebody already learned this token is dead. Learning it again
			// costs a round trip and teaches nobody anything.
			s.metrics.CredentialDead(DeathRejected)
			return roundResult{done: true}, s.unusable(cberrors.Wrapf(ErrRefreshRejected, "%s recorded that the provider refused this credential", att.Holder))

		case att.live(now):
			// Not ours to take. Nothing was presented, so this re-reads.
			return roundResult{waitingOn: att.Holder}, nil

		case att.TakeoverOf != "":
			// The one takeover per generation is spent. A second would be a
			// third presentation of a token whose first two outcomes are both
			// unknown.
			s.metrics.CredentialDead(DeathOutcomeUnknown)
			return roundResult{done: true}, s.unusable(cberrors.Wrapf(ErrRefreshOutcomeUnknown,
				"%s took over %s's unsettled refresh and did not settle either, so this token may already be spent",
				att.Holder, att.TakeoverOf))

		default:
			// An expired, unresolved, not-yet-taken-over attempt: its holder
			// died mid-refresh, which at deploy time is ordinary rather than
			// exotic. Taking over once recovers it; see the package doc for
			// why once is safe and twice is not.
			takeoverOf = att.Holder
		}
	}

	leaseState := refreshState{
		Serial:     state.Serial,
		LastWriter: state.LastWriter,
		Attempt: &attempt{
			Holder:         s.holder,
			StartedAt:      now,
			LeaseExpiresAt: now.Add(s.leaseTTL),
			Serial:         state.Serial,
			TakeoverOf:     takeoverOf,
		},
	}
	leaseBytes, err := encodeRefreshState(leaseState)
	if err != nil {
		return roundResult{done: true}, err
	}
	leaseVersion, err := s.store.Put(ctx, map[string][]byte{StateKey: leaseBytes}, version)
	if err != nil {
		if stderrors.Is(err, work.ErrVersionConflict) {
			// Contention, and nothing has been presented. The next read
			// usually finds a token somebody else already rotated.
			return roundResult{}, nil
		}
		return roundResult{done: true}, cberrors.Wrap(err, "taking the codex refresh lease")
	}
	if takeoverOf != "" {
		s.metrics.Takeover()
		s.log.ErrorContext(ctx, "taking over an unsettled codex refresh, whose token may already be spent",
			"holder", s.holder, "takeover_of", takeoverOf, "serial", state.Serial)
	}

	// A worker draining on SIGTERM must not begin something it cannot finish.
	if err := ctx.Err(); err != nil {
		s.releaseLease(ctx, state, leaseState, leaseVersion)
		return roundResult{done: true}, cberrors.Wrap(err, "cancelled before presenting the codex refresh token")
	}

	return s.present(ctx, cred, state, leaseState, leaseVersion)
}

// present hands the refresh token to the provider and acts on what came back.
func (s *Source) present(ctx context.Context, cred credentialFile, state, leaseState refreshState, leaseVersion work.SecretVersion) (roundResult, error) {
	// Logged before the call, not after: the token is spent the instant the
	// request lands, so evidence written only on success is missing in exactly
	// the case that needs it.
	s.log.InfoContext(ctx, "presenting the codex refresh token",
		"holder", s.holder, "serial", state.Serial, "lease_expires_at", leaseState.Attempt.LeaseExpiresAt)

	rctx, cancel := context.WithTimeout(ctx, s.refreshTimeout)
	defer cancel()
	res, outcome, err := s.refresher.Refresh(rctx, cred.refresh)
	s.metrics.RefreshOutcome(outcome)

	switch outcome {
	case RefreshNotSent:
		// DNS failure, connection refused, TLS handshake failure. The token
		// was definitely not presented, so this stays an ordinary blip rather
		// than a manual browser login — which is what makes the strictness
		// everywhere else affordable.
		s.releaseLease(ctx, state, leaseState, leaseVersion)
		return roundResult{done: true}, cberrors.Wrap(err, "the codex refresh request never reached the provider")

	case RefreshUnknown:
		// Deliberately left unresolved. The marker is what stops the next
		// caller, and the next process, presenting a token that may already
		// be spent.
		s.metrics.CredentialDead(DeathOutcomeUnknown)
		s.log.ErrorContext(ctx, "a codex refresh reached the provider with no usable answer, so its token may already be spent",
			"holder", s.holder, "serial", state.Serial, "cause", err, "remedy", remedy)
		return roundResult{done: true}, s.unusable(cberrors.Wrap(err, ErrRefreshOutcomeUnknown.Error()))

	case RefreshRejected:
		s.metrics.CredentialDead(DeathRejected)
		s.settleRejected(ctx, state, leaseState, leaseVersion)
		return roundResult{done: true}, s.unusable(cberrors.Wrap(err, ErrRefreshRejected.Error()))

	case RefreshReused:
		// Something else presented this token. Re-seeding without finding it
		// first just hands the second holder a fresh credential to spend, so
		// this reports the violation rather than the refusal.
		s.metrics.CredentialDead(DeathSingleWriterViolated)
		s.settleRejected(ctx, state, leaseState, leaseVersion)
		s.log.ErrorContext(ctx, "INV-1 violated: the provider reports this refresh token was already presented elsewhere",
			"holder", s.holder, "serial", state.Serial, "remedy", "find the other holder before re-seeding, or it will spend the replacement too")
		return roundResult{done: true}, s.unusable(cberrors.Wrap(err, ErrSingleWriterViolated.Error()))

	case RefreshRotated:
		file, err := s.settle(ctx, cred, state, leaseState, leaseVersion, res)
		return roundResult{file: file, done: true}, err
	}
	// Unreachable: the switch is exhaustive and the linter enforces it. An
	// outcome from the future is treated as unknown, which is the only safe
	// reading of a value this code does not understand.
	s.metrics.CredentialDead(DeathOutcomeUnknown)
	return roundResult{done: true}, s.unusable(cberrors.Wrapf(ErrRefreshOutcomeUnknown, "unrecognised outcome %s", outcome))
}

// settle stores the rotated pair and clears the lease, in one write.
func (s *Source) settle(ctx context.Context, cred credentialFile, state, leaseState refreshState, leaseVersion work.SecretVersion, res Refreshed) (work.CredentialFile, error) {
	// Everything after a presentation runs under a deadline derived from the
	// lease we hold. Without one, an Update against a wedged apiserver hangs
	// at TCP level, our lease expires under us, and another holder concludes
	// we are dead and presents the token we already spent. Floored at one
	// presentation's worth of time, because a rotated pair that is not yet
	// durable must still get a fair attempt even if the lease has run out —
	// losing it is worse than writing late.
	budget := leaseState.Attempt.LeaseExpiresAt.Sub(s.clock.Now())
	if budget < s.refreshTimeout {
		budget = s.refreshTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	ourSerial := state.Serial + 1
	rotated, authBytes, err := cred.withRotation(res, s.clock.Now())
	if err != nil {
		return work.CredentialFile{}, s.credentialLost(err)
	}
	stateBytes, err := encodeRefreshState(refreshState{Serial: ourSerial, LastWriter: s.holder})
	if err != nil {
		return work.CredentialFile{}, s.credentialLost(err)
	}
	// One write, so the rotated credential and the cleared lease share a
	// linearization point. Preconditioned on the version our own lease write
	// produced, so nothing that landed in between is silently adopted.
	values := map[string][]byte{CredentialKey: authBytes, StateKey: stateBytes}

	if _, err := s.store.Put(ctx, values, leaseVersion); err != nil {
		return s.recoverSettle(ctx, values, state, ourSerial, rotated, res, err)
	}
	s.log.InfoContext(ctx, "rotated the codex credential", "holder", s.holder, "serial", ourSerial)
	return s.usable(rotated, res)
}

// recoverSettle works out whether a failed settle actually landed.
//
// It keys on a serial and a writer identity THIS process chose and wrote, never
// on comparing token bytes. "My own write landed and the response was lost" is
// the likeliest failure in the system, and a check that could not tell it from
// a foreign writer would turn the commonest blip into the most expensive
// outcome there is.
func (s *Source) recoverSettle(
	ctx context.Context,
	values map[string][]byte,
	prev refreshState,
	ourSerial int64,
	rotated credentialFile,
	res Refreshed,
	cause error,
) (work.CredentialFile, error) {
	backoff := retry.Policy{InitialDelay: s.storeBackoff, Multiplier: 2}
	for attempt := range s.storeAttempts - 1 {
		if err := s.clock.Sleep(ctx, backoff.Delay(attempt)); err != nil {
			return work.CredentialFile{}, s.credentialLost(cberrors.Wrap(err, "cancelled while storing a rotated credential"))
		}

		observed, version, err := s.store.Get(ctx)
		if err != nil {
			cause = err
			continue
		}
		state, err := parseRefreshState(observed[StateKey])
		if err != nil {
			return work.CredentialFile{}, s.unusable(err)
		}

		switch {
		case state.Serial == ourSerial && state.LastWriter == s.holder:
			s.log.WarnContext(ctx, "a codex rotation was stored but its confirmation was lost; recovered by reading it back",
				"holder", s.holder, "serial", ourSerial)
			return s.usable(rotated, res)

		case state.Serial == prev.Serial && state.Attempt != nil &&
			state.Attempt.Holder == s.holder && state.Attempt.Serial == prev.Serial:
			// Our lease is still there and the generation has not moved, so
			// the write did not land: the version moved for a foreign reason,
			// or not at all.
			if _, err := s.store.Put(ctx, values, version); err != nil {
				cause = err
				continue
			}
			s.log.InfoContext(ctx, "rotated the codex credential", "holder", s.holder, "serial", ourSerial)
			return s.usable(rotated, res)

		case state.Serial == prev.Serial && state.Attempt != nil && state.Attempt.TakeoverOf == s.holder:
			// Our lease expired mid-settle and somebody took it over. That is
			// not a foreign writer, and sending an operator hunting one wastes
			// the only person who can fix this.
			s.metrics.CredentialDead(DeathCredentialLost)
			s.log.ErrorContext(ctx, "a rotated codex credential could not be stored before another holder took the lease over",
				"holder", s.holder, "serial", ourSerial, "taken_over_by", state.Attempt.Holder, "remedy", remedy)
			return work.CredentialFile{}, s.credentialLost(cberrors.Errorf("%s took the lease over before the rotation could be stored", state.Attempt.Holder))

		default:
			s.metrics.CredentialDead(DeathSingleWriterViolated)
			s.log.ErrorContext(ctx, "INV-1 violated: something other than this source rotated the codex credential",
				"our_holder", s.holder, "our_serial", ourSerial,
				"observed_writer", state.LastWriter, "observed_serial", state.Serial, "remedy", remedy)
			return work.CredentialFile{}, s.unusable(cberrors.Wrapf(ErrSingleWriterViolated,
				"expected serial %d written by %s, found serial %d written by %q", ourSerial, s.holder, state.Serial, state.LastWriter))
		}
	}

	s.metrics.CredentialDead(DeathCredentialLost)
	s.log.ErrorContext(ctx, "a rotated codex credential could not be stored; the previous refresh token is already spent",
		"holder", s.holder, "serial", ourSerial, "cause", cause, "remedy", remedy)
	return work.CredentialFile{}, s.credentialLost(cause)
}

// usable checks that a rotated token is worth returning, having already
// stored it, and derives the access-only copy from the rotated file.
//
// The derivation runs on the ROTATED document, not the one that was read, so
// the adapter receives the token this rotation just produced.
func (s *Source) usable(rotated credentialFile, res Refreshed) (work.CredentialFile, error) {
	if res.AccessToken.Reveal() == "" {
		// The rotation is stored by the time we get here, so the credential
		// chain is intact and this is emphatically not a re-seed. There is
		// simply nothing to hand out, and looping to ask again would spend the
		// chain a link at a time.
		s.metrics.CredentialDead(DeathNoAccessToken)
		return work.CredentialFile{}, cberrors.Wrap(work.ErrPermanent,
			"the codex credential rotated and was stored, but the provider returned no access token to use")
	}
	exp, err := expiryOf(res.AccessToken)
	if err != nil {
		s.metrics.CredentialDead(DeathUnseeded)
		return work.CredentialFile{}, s.unusable(cberrors.Wrapf(ErrUnseeded, "%v", err))
	}
	if !s.clock.Now().Add(s.margin).Before(exp) {
		// A provider behaviour change, not a bug of ours. The pair is stored
		// either way — dropping it would spend a single-use token for nothing
		// but returning it would hand over a credential that can expire during
		// the bounded model operation.
		s.metrics.CredentialDead(DeathCredentialLost)
		return work.CredentialFile{}, s.unusable(cberrors.Wrapf(ErrRefreshTooShortLived,
			"it expires at %s, inside the %s refresh margin", exp.Format(time.RFC3339), s.margin))
	}
	// The same failure as round's, classified the same way. Returning it bare
	// here made one condition two errors depending on which path reached it,
	// and this is the path where the rotation is already stored and the old
	// refresh token already spent — the caller reading it needs the remedy
	// more, not less.
	file, err := rotated.accessOnly()
	if err != nil {
		return work.CredentialFile{}, s.unusable(err)
	}
	return file, nil
}

func (s *Source) credentialLost(cause error) error {
	return s.unusable(cberrors.Wrapf(ErrCredentialLost, "%v", cause))
}

// releaseLease clears an attempt that provably presented nothing, so the next
// caller need not wait out the TTL.
//
// Best effort: an unresolved marker is a correct if vaguer signal, and failing
// the call over a failed cleanup would report a problem that does not exist. It
// detaches from the caller's context because releasing is exactly what must
// still happen when that context is what cancelled us.
func (s *Source) releaseLease(ctx context.Context, prior, leaseState refreshState, version work.SecretVersion) {
	// Releasing clears OUR attempt. It must not clear the one we took over
	// from: that holder's outcome is still unknown, and its marker is the only
	// thing bounding takeover at a single presentation. We reached here having
	// presented nothing, so restoring it hands the unspent takeover to whoever
	// comes next rather than erasing the budget and letting everyone have one.
	release := refreshState{Serial: leaseState.Serial, LastWriter: leaseState.LastWriter}
	if leaseState.Attempt != nil && leaseState.Attempt.TakeoverOf != "" {
		release = prior
	}
	cleared, err := encodeRefreshState(release)
	if err != nil {
		s.log.WarnContext(ctx, "could not encode a released codex refresh lease", "holder", s.holder, "error", err)
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.refreshTimeout)
	defer cancel()
	if _, err := s.store.Put(rctx, map[string][]byte{StateKey: cleared}, version); err != nil {
		s.log.WarnContext(ctx, "could not release the codex refresh lease; it will expire on its own",
			"holder", s.holder, "lease_expires_at", leaseState.Attempt.LeaseExpiresAt, "error", err)
	}
}

// settleRejected records a refusal so the next caller need not learn it again
// by presenting a token already known to be dead.
func (s *Source) settleRejected(ctx context.Context, state, leaseState refreshState, version work.SecretVersion) {
	settled := leaseState
	settled.Attempt = &attempt{
		Holder:         s.holder,
		StartedAt:      leaseState.Attempt.StartedAt,
		LeaseExpiresAt: leaseState.Attempt.LeaseExpiresAt,
		Serial:         state.Serial,
		TakeoverOf:     leaseState.Attempt.TakeoverOf,
		Outcome:        outcomeRejected,
	}
	encoded, err := encodeRefreshState(settled)
	if err != nil {
		s.log.WarnContext(ctx, "could not encode a rejected codex refresh outcome", "holder", s.holder, "error", err)
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.refreshTimeout)
	defer cancel()
	if _, err := s.store.Put(rctx, map[string][]byte{StateKey: encoded}, version); err != nil {
		s.log.WarnContext(ctx, "could not record that the provider refused the codex refresh token", "holder", s.holder, "error", err)
	}
	s.log.ErrorContext(ctx, "the provider refused the codex refresh token; it is spent or revoked",
		"holder", s.holder, "serial", state.Serial, "remedy", remedy)
}
