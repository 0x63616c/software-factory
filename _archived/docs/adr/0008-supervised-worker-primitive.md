# ADR-0008: Build our own supervised-worker primitive; reject actor frameworks

- Status: Accepted
- Date: 2026-07-23

## Context
The factory does many things concurrently — not distributed, but human-scale: lots of
"go do this thing" goroutines (runs, background workers, the TUI loop). Raw `go func(){}`
is easy to *start* and miserable to *stop and observe*: managed shutdown, cancellation,
error surfacing, and the per-unit recover boundary from [ADR-0006] all have to live
somewhere consistent. The question was whether to adopt an actor framework or build a
small primitive.

## Decision
**Build our own supervised-worker primitive** — a thin, legible house-style layer over
`context` + `golang.org/x/sync/errgroup` + a panic-recover boundary + `slog`. Its
responsibilities:

- spawn a *named* unit of work bound to a `context`,
- a panic-recover boundary so it cannot take down the process ([ADR-0006]),
- report its terminal error to a root supervisor,
- cancel on context, and **drain on SIGTERM with a timeout** (graceful shutdown),
- log its lifecycle transitions structurally (operability; see the logging platform
  tenet).

Everything long-running uses it. It is the **first entry in the runtime spine** and the
*mechanical* form of the [ADR-0006] recover boundary. It is a textbook case of the
"internal micro-library" tenet: one repeating pattern (safe long-running work) extracted
into one thing that does one job well.

## Rejected alternatives
Evidence was pulled from the actual repos before deciding:

- **`anthdm/hollywood` and `asynkron/protoactor-go`.** Rejected on two independent
  grounds:
  1. **Scale mismatch.** Both are *distributed-systems* frameworks — remote actors,
     clustering, network serialization (protoactor mandates protobuf; Hollywood requires
     it for remote). We are one process on one machine. Importing either is
     Kubernetes-to-run-three-processes: we'd use ~5% and pay the full conceptual cost —
     a direct legibility tax.
  2. **Dynamically-typed messages.** Both dispatch via `switch ctx.Message().(type)`.
     That is untyped: add a message, forget a handler, and the compiler says nothing —
     exactly the "I forgot to check this enum" sharp edge we ban. Our primitive uses
     typed channels and exhaustive switches, where the compiler *does* catch it.
  - `protoactor-go` additionally is still pre-1.0 with a self-declared unstable API —
     disqualifying for a factory's spine on its own.
- **Raw goroutines with no standard primitive.** Rejected: every worker hand-rolls its
  own lifecycle/recover/cancel, inconsistently and slightly wrong each time — fails
  "make correctness mechanical".

The verdict shape mirrors [ADR-0004]: **adopt the actor *idea* (isolated units, message
passing, supervision), reject the actor *framework's* worldview.** Idiomatic Go
(goroutines + channels + context = CSP) already gets us ~80% of the actor model natively.

## Consequences
- The primitive is specced as part of the runtime spine (signals → graceful shutdown,
  goroutine ownership, injectable clock, LLM seam).
- Logging is baked into the primitive (platform-feature tenet), so no worker can forget
  to log its lifecycle.
- Typed messaging keeps `exhaustive` enforcement meaningful across concurrent units.
