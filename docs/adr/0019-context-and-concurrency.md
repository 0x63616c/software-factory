# ADR-0019: Context propagation and concurrency discipline

- Status: Accepted
- Date: 2026-07-23

## Context
The factory is concurrent (supervised workers, [ADR-0008]) and long-running. Cancellation
and race-freedom aren't optional extras — an autonomous system you can't cancel cleanly
or that races on shared state is neither operable nor correct.

## Decision
- **`context.Context` is the first parameter** of any function that does I/O, blocks, or
  spawns work — named `ctx`, passed explicitly, propagated all the way down.
- **Context is never stored in a struct** (enforced by `containedctx`); it flows as an
  argument. No `context.TODO()`/`context.Background()` outside the composition root and
  tests (`contextcheck`/`fatcontext`).
- **Cancellation is always honored.** Long-running loops select on `ctx.Done()`; blocking
  calls take the context. This is what makes the supervised worker's drain-on-SIGTERM
  ([ADR-0008]) actually work.
- **`go test -race` always** — in the local test command, the lefthook pre-push hook, and
  CI. A data race is a build-breaking defect, not a warning.
- **Concurrency by ownership.** Prefer a single owner mutating state and communicating via
  channels over shared mutable state. Where shared state is unavoidable, it is guarded by
  a mutex and the ownership is documented. No ambient globals.
- **No naked goroutines.** Long-running concurrent work goes through the supervised-worker
  primitive ([ADR-0008]), never a bare `go func(){}` — so it has a lifecycle, a recover
  boundary, and cancellation.

## Rejected alternatives
- **Storing `ctx` on a struct** — hides the cancellation channel, breaks propagation,
  and is a known Go anti-pattern; `containedctx` bans it.
- **`context.Background()` deep in the call tree** — severs cancellation from the caller.
- **Shared mutable state with ad-hoc locking** — the classic race source; ownership +
  channels is more legible and `-race` keeps us honest.
- **Running `-race` only occasionally** — races are non-deterministic; only always-on
  detection catches them before they reach an overnight run.

## Consequences
- The `-race` requirement makes tests slightly slower — an acceptable operability cost.
- Ties directly to [ADR-0008] (supervised workers own goroutine lifecycles) and the
  graceful-shutdown story.
