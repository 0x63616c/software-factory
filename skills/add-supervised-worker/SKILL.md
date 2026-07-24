---
name: add-supervised-worker
description: Use when spawning any long-running concurrent work (a run, a background loop, an LLM job) — routes it through the supervised-worker primitive instead of a raw goroutine.
---

# Adding a supervised worker

**Never `go func(){}` for long-running work** — raw goroutines start easily but are
miserable to stop and observe. All long-running units go through the supervised-worker
primitive ([ADR-0008]): a thin layer over `context` + `errgroup` + a panic-recover
boundary + `slog`.

## What the primitive gives you (don't re-implement)
- A **named** unit bound to a `context.Context`.
- A **panic-recover boundary** — a panic logs the chain, marks the unit failed, reports
  to the supervisor. Never takes down the process (tenet 8).
- **Cancellation** on context + **drain on SIGTERM with a timeout**.
- **Lifecycle logging free** — start/stop/fail logged with the run-id attr. Don't hand-log
  lifecycle (tenet 9).

## Steps
1. Define the work as a function that respects its `context` (checks `ctx.Done()`, passes
   `ctx` to every blocking call — DB, LLM, shell).
2. Spawn through the supervisor with a **name** + typed inputs. Don't invent your own
   goroutine/recover/log scaffolding.
3. Communicating? **Typed channels + exhaustive switches** — never `any`-typed dispatch
   (the actor-framework sharp edge we rejected).
4. Report events through the injected sink (e.g. `EventSink` for anything the TUI must see
   — the engine never imports bubbletea, [ADR-0011]).
5. Test it: plain Go + injected fakes (fake clock, fake llm). Assert it cancels cleanly
   and reports its terminal error. No real world.

## Do not
- Spawn a bare goroutine for anything outliving a single function call.
- Swallow a panic with a silent `recover` (tenet 2) — the primitive's boundary logs and
  reports; yours must not hide.
