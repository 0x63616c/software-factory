// Package id mints and parses Stripe-style, ULID-backed identifiers of the form
// "<prefix>_<ulid>" — e.g. "run_01J9Z3QK8XV2M7…".
//
// The prefix makes an ID self-describing: you know what an ID refers to on sight, in a
// log line or a DB row (legibility + operability). The ULID body sorts chronologically
// (lexical order == creation order).
//
// A Generator bundles the two non-deterministic edges ID-minting needs — the injected
// clock (for the sortable timestamp) and an injected entropy source — so both are
// controlled and swappable (SoftwareStyle testability floor). Wire it once at the
// composition root with clock.System{} + crypto/rand.Reader; in tests, hand it a fake
// clock + a deterministic reader for byte-exact, reproducible IDs.
//
// Each entity wraps a Generator in its own un-forgeable typed ID (e.g. run.RunID) so
// the compiler stops you passing one entity's ID for another's.
package id

import (
	"io"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/oklog/ulid/v2"

	"github.com/0x63616c/software-factory/internal/clock"
)

// Generator mints IDs from an injected clock and entropy source. Both edges are
// controlled, so IDs are sortable (clock) and reproducible in tests (entropy).
type Generator struct {
	clk     clock.Clock
	entropy io.Reader
}

// NewGenerator wires a Generator. Production: NewGenerator(clock.System{}, rand.Reader).
// Tests: NewGenerator(clock.NewFake(t), deterministicReader).
func NewGenerator(clk clock.Clock, entropy io.Reader) *Generator {
	return &Generator{clk: clk, entropy: entropy}
}

// New mints a fresh "<prefix>_<ulid>".
func (g *Generator) New(prefix string) string {
	body := ulid.MustNew(ulid.Timestamp(g.clk.Now()), g.entropy)
	return prefix + "_" + body.String()
}

// Parse checks that s carries the expected prefix and a well-formed ULID body, and
// returns the parsed ULID. It is the boundary guard for IDs arriving from the DB, an
// API, or user input — a wrong prefix or malformed body is an error, never silently
// accepted. It needs no Generator: parsing is pure.
func Parse(prefix, s string) (ulid.ULID, error) {
	body, ok := strings.CutPrefix(s, prefix+"_")
	if !ok {
		return ulid.ULID{}, errors.Newf("id %q: missing %q_ prefix", s, prefix)
	}
	u, err := ulid.Parse(body)
	if err != nil {
		return ulid.ULID{}, errors.Wrapf(err, "id %q: malformed ULID body", s)
	}
	return u, nil
}
