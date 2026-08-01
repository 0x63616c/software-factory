# ADR-0001: Priority ordering — Legibility > Correctness > Operability > Economy

- Status: Accepted
- Date: 2026-07-23

## Context
A style guide is only useful when two good rules conflict and something has to give.
TigerStyle's power is not its rules but its *ranking* (safety > performance > DX): the
ordering decides. We needed the equivalent root ordering for a software factory — an
agent-operated TUI that takes tickets and ships code to production.

The novel first-class concern here is **legibility**: whether a coding agent (and the
human reviewing it) can understand, verify, and safely change any piece without holding
the whole system in their head. This is different from a database's safety/performance
axis. Note the scoping (see [ADR-0003]): these values govern the factory's *own* code
(Layer A), not the quality of the code the factory emits (Layer B).

## Decision
When a trade-off must be made, resolve it in this order, higher beats lower:

> **Legibility > Correctness > Operability > Economy**

Machine performance (cache-friendliness, allocation, sub-microsecond work) is **not on
the list**. This is a human-scale, LLM-latency-bound system; below ~1s we do not care.
"Don't be stupid" is the only machine-perf rule, and it is not a ranked value.

Definitions:
- **Legibility** — a piece can be understood *and verified* in isolation, without
  loading the whole system. Deep modules, narrow interfaces, intent-revealing names,
  invariants as assertions, no hidden global state.
- **Correctness** — the program does what it's specified to do and, when it can't,
  fails *loud and early* rather than limping. Validate at boundaries; assert invariants;
  no empty catch; no default-to-zero fallback that hides a bug.
- **Operability** — while running autonomously you can *see* what it's doing and
  *stop/reverse* it. Structured logs at decisions, observable state,
  idempotent/reversible steps, clean cancellation.
- **Economy** — LLM spend + human-scale wait time. This is *architectural* (fewer/
  cheaper calls, caching, batching, model choice), not micro-optimization. It is the
  tiebreaker among otherwise-equal designs.

The explicit adjacent trades:
- **Legibility beats Correctness.** Prefer a paranoid assertion that "can't fire" or
  three obvious functions over one clever one. An agent maintains this code; what it
  can't understand it can't correctly change, so illegible-but-correct rots into
  incorrect on the next edit.
- **Correctness beats Operability.** A broken invariant mid-run halts; we do not
  "log and keep going." A factory running on corrupted state produces confident garbage.
- **Operability beats Economy.** Structured logs at every decision and idempotent steps
  cost cycles and tokens; pay them. You cannot run an autonomous code-shipping system
  you cannot watch and stop.
- **Economy last.** It wins only among equally legible/correct/operable options: you
  *will* spend tokens on a verification pass (correctness), but not on a third redundant
  pass that buys nothing.

## Rejected alternatives
- **Correctness first (TigerStyle-shaped: assert everything, fail fast above all).**
  Rejected as the *top*: for a codebase maintained by agents, illegible code is the
  root cause of future incorrectness. Legibility is upstream of durable correctness.
  Correctness remains #2 and loud-failure is still a core tenet.
- **Performance as a ranked value.** Rejected. It conflated two things: machine
  efficiency (irrelevant here, dropped) and Economy (LLM cost + wait, kept and renamed).
- **Treating "ships bad code" as the cardinal correctness sin.** Rejected as a category
  error — output quality is Layer B (the target's standards), not Layer A. See [ADR-0003].

## Consequences
- Every downstream decision inherits this ordering; ADRs cite it when resolving a trade.
- Design reviews (human or agent) can appeal to a concrete ranking rather than taste.
- Testability is treated separately as a non-negotiable floor, not a ranked value —
  see [ADR-0002].
