# ADR-0004: Manual constructor injection, one composition root

- Status: Accepted
- Date: 2026-07-23

## Context
The factory has many collaborating parts (store, LLM client, supervised workers, TUI)
and needs a way to wire them. Dependency injection was flagged as a big decision,
including whether to adopt a framework. This decision is really a legibility bet: how
dependencies are wired determines whether an agent can answer "where does this value come
from?" by *reading*.

## Decision
**Manual constructor injection with a single composition root.** Every dependency is a
plain constructor argument; `cmd/factory/main.go` assembles the whole graph explicitly,
by hand, in one place. No magic, no reflection — you read the wiring.

`google/wire` (compile-time DI codegen) may be adopted **later, only if** the composition
root becomes genuinely painful — it generates readable Go and is compile-time safe, so it
stays consistent with our values.

## Rejected alternatives
- **Runtime DI containers (`uber/fx`, `samber/do`).** Rejected. Reflection builds the
  graph at startup, so you cannot answer "where does this come from?" by reading — only by
  running. That is exactly the "where did this come from" sharp edge we are eliminating,
  and it fights both Legibility (#1) and the "make correctness mechanical" tenet
  (runtime magic over compile-time checks). Same verdict shape as rejecting a runtime
  actor framework ([ADR-0008]).
- **`google/wire` from day one.** Not now: adds a codegen step before we have proven the
  hand-wiring hurts. Kept in reserve.
- **Raw ad-hoc construction with no discipline.** Rejected: inconsistent wiring is a
  legibility loss.

## Consequences
- The composition root doubles as the single binary's `main` ([ADR-0005]).
- This choice is *driven by* the testability floor ([ADR-0002]): manual injection lets a
  test hand a fake LLM or fake clock to a constructor with zero magic. That is the whole
  point — DI is not ideology here, it is what makes testability work.
- Accepts one known cost: the composition root grows more verbose as the factory grows.
  The escape hatch (wire) is compile-time and readable, not a runtime container.
