# ADR-0012: Testing tiers, and how we test the LLM

- Status: Accepted
- Date: 2026-07-23

## Context
Agents write this code, so the test suite is their feedback loop (`change → run test →
read → decide`). We use ginkgo + gomega. The hard part is LLM-driven code:
non-deterministic, slow, and it costs money (the Economy axis) — it cannot sit in the
fast loop.

## Decision
**Three tiers:**
- **Unit** (the bulk, fast, always-on): pure Go, no real world, `:memory:` sqlite,
  `Update` functions called directly. Runs in seconds on every change.
- **Integration** (smaller, still deterministic): real sqlite *file*, real git against
  temp repos, supervised-worker lifecycle. No live LLM.
- **E2E smoke** (few): `teatest` (in-Go, white-box TUI logic) and `tu` (real terminal,
  the built binary).

**The LLM:**
- **(a) Fake at the interface — default.** The `llm` seam is an interface; tests inject
  a fake returning canned responses. Tests orchestration logic deterministically, free.
- **(b) Record/replay cassettes** for seams that parse real model output — capture once,
  replay in tests. Real-shaped, deterministic, free on replay.
- **(c) Live-LLM tier** — opt-in, tagged, off by default, never in the fast loop.

**The LLM is never in the unit loop.**

**Coverage:** gate the high-value **engine/domain** packages (CI fails on a drop);
**no global gate** on TUI/glue/generated code. Exact numbers deferred until packages exist.

## Rejected alternatives
- **Live LLM in unit tests**: rejected — non-deterministic, slow, expensive.
- **Blunt global coverage gate**: rejected — it just makes agents write fake tests to
  chase the number, worse than no gate.

## Consequences
Test production is cheap in an LLM era, so we bias toward thorough. `:memory:` (ADR-0010)
is the unit-tier database and is testability-floor-compliant (ADR-0002).
