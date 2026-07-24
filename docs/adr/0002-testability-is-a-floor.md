# ADR-0002: Testability is a floor, not a ranked value

- Status: Accepted
- Date: 2026-07-23

## Context
Because agents write and maintain this codebase, testability is unusually important: the
agent's whole loop is *change → run test → read result → decide*. The test suite is its
eyes. The question was where testability sits relative to the priority ordering in
[ADR-0001].

## Decision
Testability is a **floor, not a dial**. It is *not* ranked among Legibility / Correctness
/ Operability / Economy, because you would never trade it away to buy one of them — that
would be a category error. It is a non-negotiable property the whole codebase must have,
sitting underneath the ranking like a building code.

The concrete rule:

> **No unit test may touch the real world.** Every external edge — the LLM, shell-outs,
> the clock, the filesystem, the network, the terminal — sits behind a narrow injectable
> interface, so a test hands it a fake. Tests are deterministic: injected clock, no real
> time/randomness/network/model calls.

In-memory sqlite (`:memory:`) is explicitly **not** "the real world" in the forbidden
sense: it is hermetic, deterministic, and spun fresh per test. Prefer it to mocking the
store (it tests the actual SQL — higher fidelity than a mock). See [ADR-0010].

## Rejected alternatives
- **List testability inside the priority ordering (as a fifth value) for emphasis.**
  Rejected: ranking it invites the mistake of "trading" it against another value, which
  we never do. A floor communicates "never negotiable" more accurately than a rank.
- **Rely on integration/e2e tests for confidence.** Rejected as the primary tier: they
  are slow and non-deterministic, so they cannot be the agent's tight feedback loop.
  They exist as a smaller separate tier (see [ADR-0012]).

## Consequences
- The testability floor is the direct reason for manual constructor injection
  ([ADR-0004]): it lets a test hand a fake to a constructor with zero magic.
- The same design properties that make a module legible (deep, narrow interface, no
  hidden state) are what make it testable — testability and legibility ([ADR-0001]) are
  one design move seen from two angles.
- It sets up the test tiers and the LLM record/replay strategy in [ADR-0012].
- TDD is mandated test-first for engine/domain code as the procedural form of this floor.
