# ADR-0017: Stripe-style, ULID-backed, typed identifiers minted through a Generator

- Status: Accepted
- Date: 2026-07-23

## Context
Entities need IDs. Bare `string` IDs are a triple failure: you can't tell what an ID
refers to on sight, you can pass one entity's ID where another's is expected, and they
don't sort. We want IDs that are self-describing, un-confusable, and sortable — and
that are deterministic in tests (testability floor, [ADR-0002]).

## Decision
IDs have the form **`<prefix>_<ulid>`** — e.g. `run_01J9Z3QK8XV2M7…` — combining three
wins:

- **Prefix = self-describing type.** You know what an ID is on sight, in a log or a DB
  row (legibility + operability). Prefixes are assigned when domains are named (TBD).
- **ULID body = sortable + time-ordered.** Crockford base32, no ambiguous characters, no
  dashes (double-click-selectable). Lexical order == creation order, so entities list
  chronologically with no separate `created_at` sort.
- **Typed wrapper per entity = illegal-states-unrepresentable.** Each entity defines its
  own ID as a struct with an **unexported field**, so a value can only be produced by
  `NewXxxID`/`ParseXxxID` — it cannot be forged from a raw string, and the compiler
  refuses to pass one entity's ID for another's.

The generic engine is `internal/id`, a **`Generator`** that bundles the two
non-deterministic edges ID-minting needs — the injected `clock.Clock` (sortable
timestamp) and an injected `io.Reader` entropy source:

```go
gen := id.NewGenerator(clock.System{}, crypto/rand.Reader) // prod (composition root)
gen := id.NewGenerator(clock.NewFake(t), deterministicReader) // test → reproducible IDs
```

Randomness is injected (not just the clock) for consistency with the testability floor,
for byte-exact reproducible IDs when a test wants them, and to keep a controlled
non-determinism boundary open. `crypto/rand`/`math/rand` are import-banned outside the
composition root (depguard) so entropy is always the injected one. Storage is the full
string (readable, sortable, greppable). Input from outside (DB/API/user) goes through
`ParseXxxID`, which rejects a wrong/missing prefix at the boundary ([ADR-0024]).

## Rejected alternatives
- **Bare `string` IDs** — not self-describing, confusable, unsortable.
- **UUIDv4** — not sortable, has dashes (breaks double-click), no type info.
- **UUIDv7** — sortable but dash-delimited and less compact than ULID base32.
- **Opaque random body (pure Stripe)** — unguessable, but not sortable and not
  reproducible in tests. Our IDs aren't bearer-secrets, so unguessability buys little;
  sortability + test-determinism buy a lot.
- **`type RunID string` alias** — still castable from any string (`RunID("garbage")`);
  the struct wrapper's unexported field is what makes it un-forgeable.
- **Hardcoding `crypto/rand` inside the Generator** — cheaper, but closes the
  determinism/DST seam; injecting entropy costs a few lines and keeps it open.

## Consequences
- `internal/id` depends on `internal/clock` ([ADR-0002]) and `oklog/ulid/v2`.
- ID-minting requires a `*id.Generator`, which is fine: it's wired once at the
  composition root ([ADR-0004]) and rides along as a struct field wherever IDs are made.
  A pure function can't mint an ID — correct, since minting reads time + randomness.
- Production entropy should use `ulid.Monotonic` to guarantee ordering within a
  millisecond (an implementation detail of the entropy source, not the interface).
