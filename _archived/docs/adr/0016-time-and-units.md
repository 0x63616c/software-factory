# ADR-0016: Time is UTC-only; ISO-8601 on the wire; units in names

- Status: Accepted
- Date: 2026-07-23

## Context
Timezone bugs are a recurring, expensive class: a backend that stores or compares
non-UTC time clashes with a frontend in the user's zone. Separately, raw numbers that
carry a physical unit (`timeout`, `size`) are ambiguous at the call site. We want both
problems designed out.

## Decision
- **The engine is UTC-only.** Every `time.Time` created, stored, logged, and compared in
  the engine is UTC. **Only the TUI localizes** to the user's zone — the same
  engine/presentation boundary as [ADR-0011]. This is enforced structurally: `time.Now`
  is linter-banned outside `internal/clock` ([ADR-0002] testability floor), and
  `clock.Clock.Now()` *returns* UTC, so a local-zone timestamp cannot enter the engine
  even by accident. `time.Local` is also banned.
- **Serialize as RFC3339 / ISO-8601, UTC.** Every timestamp crossing a boundary (sqlite,
  logs, JSON, tickets) is RFC3339 text — human-readable in the DB and lexically sortable.
- **No sleeps in tests.** A backoff test (`1s → 10s → 1m`) must advance a fake clock,
  never wait. This requires the `Clock` seam to grow injectable timers (`After`) — that
  work is **deferred until the first retry exists** (building timer infra before there's
  a retry to test is premature); when built, the choice is hand-roll vs `jonboulle/clockwork`
  (lean clockwork — correct fake-timer fan-out is fiddly, keep our own narrow interface).
- **Relative time in the UI** ("5m ago") where it helps (activity lists), computed from
  the injected clock so it stays testable — presentation-only, not everywhere.
- **Units in names, the Go-nuanced way.** For a `time.Duration`, DON'T suffix — the type
  carries the unit (`timeout time.Duration`, never `timeoutMS`). For raw numbers that
  carry a unit but aren't a typed unit (bytes, counts, config ints, wire/DB values),
  suffix in camelCase: `sizeBytes`, `maxAgeSeconds`, `retryDelayMS`.

## Rejected alternatives
- **Local/implicit timezones in the engine** — the exact bug class we're eliminating.
- **Unix-int timestamps in storage** — not human-readable in the DB; RFC3339 sorts and
  reads better for a human-scale system.
- **`time.Sleep` in tests** — a code smell; slow suites break the agentic feedback loop
  ([ADR-0012]). Injected time is the rule.
- **Suffixing `time.Duration` with `_ms`** — redundant; the type already states the unit.

## Consequences
- `clock.Clock`/`System`/`Fake` all return UTC ([ADR-0002]).
- Units-in-names is judgment-tier (no clean linter) → the review skill catches it.
- The timer-seam requirement is documented now so it isn't forgotten when retries land.
