# AGENTS.md

Always-loaded context. Thin on purpose — in context every turn. Detail:
[`docs/SoftwareStyle.md`](./docs/SoftwareStyle.md) (tenets), [`docs/adr/`](./docs/adr/)
(decisions + why), [`skills/`](./skills/) (procedures). Read `SoftwareStyle.md` before
non-trivial work.

## What this is
Software factory: TUI that takes tickets, ships code to production, built to run on
*any* codebase — including itself. Engine is language-agnostic; standards are data. This
repo is Layer A (the factory's code) *and* Layer B (its first target) — same mechanism,
different content.

## Priority ordering (resolves every trade-off, high beats low)
**Legibility > Correctness > Operability > Economy.** Machine perf unranked (ignore
below ~1s). Definitions + trades: `SoftwareStyle.md`.

## Testability floor (never traded)
No unit test touches the real world. Every external edge (LLM, shell, clock, fs,
network, terminal) behind an injectable interface. `:memory:` sqlite fine.

## Tenets (detail in SoftwareStyle — linked, not restated)
mechanical-correctness · no-escape-hatches · deep-modules/narrow-door ·
no-leaky-abstractions · elegant-deps-within-bar · micro-libs-when-earned ·
fail-fast-helpful · panics-contained-per-unit · platform-logging · headless-engine ·
dont-rely-on-reading · single-source-of-truth.

## Dependencies
Not minimalist — prefer the elegant lib that aids legibility, within the bar (readable,
maintained, popular, permissive, small transitive, godoc-legible seam). Pin everything.
Wrap only risky/leaky. `os.Getenv` only in `config`; `time.Now`/`time.Local` only in
`clock`; entropy only in `cmd/`.

## Repo shape
Single module, single binary. `cmd/factory/` = composition root (manual constructor
injection, graph wired by hand). `internal/` by domain (deep modules), never by layer.
No `pkg/`. Domain names unfixed — don't hardcode a vocabulary.

## Operating protocol
- Branch per task; never commit `main`. PR to merge. Conventional, atomic commits.
- TDD test-first mandatory for engine/domain; preferred-not-forced for UI.
- Done = `golangci-lint` + relevant tests pass (hook-enforced; own it anyway).
- Verify before claiming done — command output, not assertion.
- Stop and ask before anything irreversible/outward-facing: force-push, `main` merge,
  deleting data / migrating down, side-effecting external calls.

## The wall
[`.golangci.yml`](./.golangci.yml) enforces the mechanical tenets. Want a rule off? Fix
the code (no-escape-hatches).
