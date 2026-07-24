# ADR-0010: Persistence — sqlite + sqlc + goose, no repository pattern

- Status: Accepted
- Date: 2026-07-23

## Context
The factory keeps state (tickets/runs/artifacts — names TBD). We want typed queries,
loud fail-fast on schema problems, and easy testing. A recurring instinct is to
"abstract storage for testing/swappability" — but sqlite testing is trivial, which
changes the calculus.

## Decision
- **Driver: `modernc.org/sqlite`** — pure Go, no cgo. Trivial cross-compile, static
  binary, no C toolchain.
- **Migrations: `goose`** — plain `-- +goose Up/Down` SQL, embedded via `//go:embed`
  (`embed.FS`), **auto-run on startup, fail-fast** (a failed migration = helpful error
  + exit, never limp). Every `Up` has a real `Down`.
- **Queries: `sqlc`** with `emit_interface: true` — typed methods generated from SQL;
  the interface is *generated*, not hand-written.
- **No repository pattern.** `sqlc`/`database/sql` types are **sealed inside
  `internal/store`** (depguard-enforced, tenet #4). The rest of the factory sees domain
  types and a narrow surface.
- **Test domain logic against real in-memory sqlite (`:memory:`)** — hermetic,
  deterministic, higher-fidelity than a mock (it runs the actual SQL). Inject the
  generated interface's fake **only to force failures** (disk-full/constraint/timeout)
  that `:memory:` won't produce on demand.

## Rejected alternatives
- **Hand-rolled repository abstraction**: tedious, leaky, fights sqlc's concrete types;
  the only real reason to keep an interface is failure-injection, and sqlc emits that
  interface for free.
- **`mattn/go-sqlite3`** (cgo): build/ops tax for no benefit here.
- **Atlas** (declarative migrations): heavier, more magic; overkill now. goose pairs
  naturally with sqlc's plain-SQL schema.

## Consequences
DB-swapping is a non-goal (YAGNI — sqlite for years). The generated interface stays for
failure-path tests, not portability.
