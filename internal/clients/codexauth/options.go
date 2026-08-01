package codexauth

import "time"

// The defaults. Each one is a number the safety argument depends on, so each
// carries its derivation rather than a value someone once picked.
const (
	// defaultRefreshMargin is how long before expiry a token is refreshed.
	//
	// It is sized for the full conservative use bound supplied to Source.New,
	// because the derived access-only document cannot refresh while in use.
	// The current 60-minute stage bound plus safety headroom yields 90 minutes.
	// Measured tokens carry multi-day lifetimes, so this costs at most one
	// extra refresh per generation.
	defaultRefreshMargin = 90 * time.Minute

	// defaultRefreshTimeout bounds one presentation of the refresh token. The
	// takeover policy rests on a presentation being bounded, so this is not a
	// nicety: an unbounded one would make an expired lease uninterpretable.
	defaultRefreshTimeout = 30 * time.Second

	// defaultLeaseTTL is how long a refresh attempt holds the lease. Ten times
	// the presentation bound: an actor that has not settled in ten times its
	// own hard timeout is not about to.
	defaultLeaseTTL = 5 * time.Minute

	// defaultLeasePoll is how long to wait before re-reading while another
	// holder is refreshing. Short relative to the lease TTL, because the
	// common case is a rotation that completes in under a second.
	defaultLeasePoll = 2 * time.Second

	// defaultWaitRounds bounds how many times one call re-reads. Only lease
	// contention and waiting on a live foreign lease consume a round; a round
	// in which the token was presented is the last one, whatever its outcome.
	defaultWaitRounds = 5

	// defaultStoreAttempts bounds the writes that follow a rotation. It is
	// generous because the token is already spent by then: every attempt here
	// is cheap and the alternative is a dead credential.
	defaultStoreAttempts = 5

	// defaultStoreBackoff is the first wait between those attempts, doubling.
	defaultStoreBackoff = 250 * time.Millisecond
)

// options is everything tunable about a Source. It is unexported: growth goes
// down into private helpers, not out into the surface.
type options struct {
	metrics        Metrics
	margin         time.Duration
	refreshTimeout time.Duration
	leaseTTL       time.Duration
	leasePoll      time.Duration
	waitRounds     int
	storeAttempts  int
	storeBackoff   time.Duration
}

// Option configures a Source.
type Option func(*options)

// WithMetrics records outcomes. Defaults to a recorder that drops them.
func WithMetrics(m Metrics) Option {
	return func(o *options) { o.metrics = m }
}

// WithRefreshMargin sets how long before expiry a token is refreshed. See
// defaultRefreshMargin for why the default is what it is; the number is
// meaningless without its derivation.
func WithRefreshMargin(d time.Duration) Option {
	return func(o *options) { o.margin = d }
}

// WithRefreshTimeout bounds one presentation of the refresh token.
func WithRefreshTimeout(d time.Duration) Option {
	return func(o *options) { o.refreshTimeout = d }
}

// WithLeaseTTL sets how long a refresh attempt holds the lease before another
// holder may take it over.
func WithLeaseTTL(d time.Duration) Option {
	return func(o *options) { o.leaseTTL = d }
}

// WithLeaseWait sets how long to wait between re-reads while another holder is
// refreshing, and how many times to do so before reporting a refresh in
// progress. Both bound how long a caller is held, so both are operationally
// visible.
func WithLeaseWait(interval time.Duration, rounds int) Option {
	return func(o *options) {
		o.leasePoll = interval
		o.waitRounds = rounds
	}
}

// WithStoreRetries bounds the writes that follow a rotation, and the first
// backoff between them.
func WithStoreRetries(attempts int, base time.Duration) Option {
	return func(o *options) {
		o.storeAttempts = attempts
		o.storeBackoff = base
	}
}
