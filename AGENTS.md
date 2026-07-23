# AGENTS.md

Canonical, always-loaded context for any agent working on this repo. Kept **thin** on
purpose — it's in context every turn. Detail lives in [`docs/SoftwareStyle.md`](./docs/SoftwareStyle.md)
(values & tenets), [`docs/adr/`](./docs/adr/) (decisions & why), and [`skills/`](./skills/)
(procedures). Read `SoftwareStyle.md` before non-trivial work.

## What this is
A **software factory**: a TUI that takes tickets and produces code merged to
production, built to operate on *any* codebase — including itself. The **engine is
language-agnostic**; all standards are data. This repo is the factory's own code
(Layer A) *and* its first target project (Layer B) — same mechanism, different content.

## Priority ordering (resolves every trade-off, high beats low)
**Legibility > Correctness > Operability > Economy.** Machine performance is not on
the list (ignore below ~1s). Definitions and the explicit trades are in `SoftwareStyle.md` under *Priority ordering*.

## Testability is a floor, never traded
No unit test touches the real world. Every external edge (LLM, shell, clock, fs,
network, terminal) sits behind an injectable interface. `:memory:` sqlite is fine.

## The rules that always apply
1. **Make correctness mechanical** — if a lint/type/codegen can enforce it, it must.
2. **No escape hatches** — no `//nolint`, no ignored errors, no bare `any`, no silent `recover`.
3. **Deep modules, narrow door** — narrow public surface, deep private internals; split by sub-capability; seal sub-packages with nested `internal/`.
4. **No leaky abstractions** — `sqlc` types stay in `store`; `bubbletea` stays in `tui`.
5. **Fail fast & helpful** — config errors are clean user-facing messages + non-zero exit, never a panic dump.
6. **Panics never escape a unit of work** — recover boundary per run/worker/TUI-loop.
7. **The engine is headless** — no domain logic in the TUI; engine↔TUI only via the `EventSink` seam.
8. **Logging is a platform feature** — baked into the primitives; verbose, run-id-correlated, to a file, written for a future debugging agent.
9. **Don't rely on reading** — enforcement pyramid: mechanical > always-loaded context > hooks > skills.

## Dependencies
Not minimalist — prefer the elegant lib that improves legibility, *within the bar*
(readable surface, maintained, popular, permissive licence, small transitive
footprint, godoc-legible seam). Pin everything. Wrap only risky/leaky deps.
Chosen: `cockroachdb/errors`, `log/slog`, `koanf`, `modernc.org/sqlite`, `goose`,
`sqlc` (codegen), `bubbletea`/`lipgloss`, `ginkgo`/`gomega`. `os.Getenv` is banned
outside `internal/config`.

## Repo shape
Single module, single binary. `cmd/factory/` is the composition root (manual
constructor injection — the whole dependency graph wired by hand in one place).
`internal/` is organized **by domain** (deep modules), never by layer. No `pkg/`.
Domain names are not yet fixed — do not hardcode a vocabulary prematurely.

## Agent operating protocol
- **Branch per task; never commit to `main`.** PR to merge. Conventional, atomic commits.
- **TDD is mandatory test-first for engine/domain code.** For UI code it's preferred
  but left to judgment — never blocked, never forced.
- **Done-loop:** a change is not "done" until `golangci-lint` and the relevant tests
  pass. This is hook-enforced, but own it regardless.
- **Verification before completion:** never claim "fixed"/"passes" without the command
  output that proves it. Evidence before assertion.
- **Stop and ask** before anything irreversible or outward-facing: force-push, merge to
  `main`, deleting data or migrating down in anger, side-effecting external calls.

## The wall
[`.golangci.yml`](./.golangci.yml) enforces the mechanical tenets. If you want to
disable a rule, fix the code instead (rule #2).
