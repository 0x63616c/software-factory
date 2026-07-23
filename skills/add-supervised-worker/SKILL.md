---
name: add-supervised-worker
description: Use when spawning any long-running concurrent work (a run, a background loop, an LLM job) — routes it through the supervised-worker primitive instead of a raw goroutine.
---

# Adding a supervised worker

**Never `go func(){}` for long-running work.** Raw goroutines are easy to start and
miserable to stop and observe. All long-running units go through the supervised-worker
primitive — the first entry in the runtime spine (ADR: supervised-worker primitive).
It's a thin house-style layer over `context` + `golang.org/x/sync/errgroup` + a
panic-recover boundary + `slog`.

## What the primitive gives you (so you don't re-implement it)
- A **named** unit bound to a `context.Context`.
- A **panic-recover boundary** — a panic in this unit logs the full chain, marks the
  unit failed, and is reported to the supervisor. It **never** takes down the process
  (tenet #8; ADR: error handling).
- **Cancellation** on context, and **drain on SIGTERM with a timeout** (graceful
  shutdown).
- **Lifecycle logging for free** — start/stop/fail are logged with the run-id
  correlation attr. You do not hand-log lifecycle (tenet #9, observability is a platform
  feature).

## Steps
1. Define the unit's work as a function that respects its `context` (checks
   `ctx.Done()`, passes `ctx` down to every blocking call — DB, LLM, shell).
2. Spawn it through the supervisor, giving it a **name** (used in logs) and its typed
   inputs. Do not invent your own goroutine/recover/log scaffolding.
3. If the unit communicates, use **typed channels + exhaustive switches** — never
   dynamic `any`-typed message dispatch (that's the actor-framework sharp edge we
   rejected; `exhaustive` lints your switch).
4. Report results/events through the injected sink (e.g. `EventSink` for anything the
   TUI must see — the engine never imports bubbletea; ADR: headless engine).
5. Test it: the unit is plain Go with injected fakes (fake clock, fake llm). Assert it
   cancels cleanly and reports its terminal error. No real world (SoftwareStyle: Testability floor).

## Do not
- Spawn a bare goroutine for anything that outlives a single function call.
- Swallow a panic with a silent `recover` — the primitive's boundary logs and reports;
  yours must not hide (tenet #2).
