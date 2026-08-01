package telemetry

import "sync"

// LabelValueLimit is how many distinct values one label key may export before
// the rest are folded together.
//
// Generous enough that no legitimate stage, model, effort or outcome set comes
// close, small enough that a leak is capped. It matches the ceiling
// packages/platform/metrics/bounded.ts uses on the TypeScript side, because the
// two feed the same Prometheus and a reader comparing them should not have to
// hold two numbers.
const LabelValueLimit = 200

// OtherLabelValue is the bucket every value beyond the limit collapses into,
// and the value an empty label is reported as.
const OtherLabelValue = "other"

// boundedLabels is the cardinality guard for label values.
//
// A label value is part of a series' identity: every distinct one allocates a
// series that lives for the process's lifetime and is stored by the server
// afterwards. This service's labels are its own config rather than anything an
// attacker supplies, so they are not a hostile input — but config is hand-typed
// into an UpdateConfig signal, Model.Validate deliberately checks only that the
// name and effort are non-empty (an allowlist would reject efforts codex
// accepts), and a typo is therefore a permanent series. A year of tuning leaves
// a year of dead series, and nothing ever removes them.
//
// So the first LabelValueLimit distinct values per key pass through as written
// and everything after becomes OtherLabelValue. That makes a leak show up as a
// conspicuous `other` bucket on a dashboard instead of as a metrics endpoint
// that grows without bound — the same trade, and the same ceiling, as
// packages/platform/metrics/bounded.ts, which the TS side applies to every
// label it exports.
//
// The state is per-Metrics rather than per-process so a test observes exactly
// what it recorded, and it is mutex-guarded because stage attempts finish on
// whatever goroutine ran them.
type boundedLabels struct {
	mu    sync.Mutex
	seen  map[string]map[string]struct{}
	limit int
}

func newBoundedLabels(limit int) *boundedLabels {
	return &boundedLabels{seen: map[string]map[string]struct{}{}, limit: limit}
}

// fold returns the value to export for key, which is value itself until key has
// spent its budget. An empty value is never exported as an empty string: a
// blank label reads as a missing dimension in a query rather than as a fact.
func (b *boundedLabels) fold(key, value string) string {
	if value == "" {
		return OtherLabelValue
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	values, ok := b.seen[key]
	if !ok {
		values = map[string]struct{}{}
		b.seen[key] = values
	}
	if _, ok := values[value]; ok {
		return value
	}
	if len(values) >= b.limit {
		return OtherLabelValue
	}
	values[value] = struct{}{}
	return value
}
