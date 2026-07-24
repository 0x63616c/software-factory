# ADR-0005: Repo shape — single module, single binary, by-domain deep modules

- Status: Accepted
- Date: 2026-07-23

## Context
Error handling, logging, persistence, and the TUI all need to know where code lives
before rules can be placed. Go has a genuine culture war over package layout
(by-layer vs by-domain, `pkg/` or not, one module or many). The layout has to serve
legibility ([ADR-0001]) for an agent that should understand one capability by opening
one place.

## Decision
- **Single Go module, single binary.** One `go.mod`; `cmd/factory/` is the one binary and
  also the composition root ([ADR-0004]). A headless daemon or second binary is added
  *then*, not now.
- **`internal/` organized by domain, not by layer.** Each domain package is a deep
  module: everything about one capability lives in one directory behind a **narrow door
  (small public surface) with a deep room (private internals)**. Growth goes *downward*
  into private sub-packages, never *outward* into the public surface.
- **Domain names are deliberately NOT fixed yet.** `ticket`/`run` are placeholders; the
  real vocabulary is pinned later (domain modeling). Do not hardcode a vocabulary
  prematurely.
- **Split signals** — split a domain when any one fires: (1) you can't name its single
  job in one sentence; (2) you `grep`/scroll to find where something lives; (3) it's past
  ~7–10 files (soft). **Split by sub-capability** (`dashboard/`, `ticketview/`), never by
  layer (`models/`, `views/`). A correct split makes the public surface *smaller*.
- **Sub-packages are sealed with nested `internal/`** on split, so the compiler enforces
  the narrow door (e.g. `internal/tui/internal/dashboard` is unimportable from outside
  `internal/tui/`). Mechanical, not convention.
- **No `pkg/`.** Nothing here is a public library for outside consumers. A genuine
  micro-library graduates to its own module/repo, not `pkg/`.

## Rejected alternatives
- **By-layer (`models/`, `services/`, `handlers/`, `repositories/`).** Rejected: to
  understand "what happens to a ticket" an agent must open four packages; every feature
  smears across every layer — the opposite of legibility.
- **Multi-module workspace now.** Rejected as a premature legibility tax (version skew,
  replace directives) with no current need.
- **Flat sub-packages by convention (no nested `internal/`).** Rejected on split: it
  leaves the narrow door to discipline instead of the compiler, against the
  "make correctness mechanical" tenet.
- **A `pkg/` directory.** Rejected as cargo-culting for a repo with no external consumers.

## Consequences
- "Where's the logic?" has one answer per capability: its domain package.
- The nested-`internal/` seal composes with `depguard` import rules ([ADR-0011]) to make
  architectural boundaries build failures.
- "Package too big" is one of the few *judgment* rules (the three tells), not a
  mechanical one — a file-count linter is too crude — so it lives as a documented
  heuristic watched in review.
