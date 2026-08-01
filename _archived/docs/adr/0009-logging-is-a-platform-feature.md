# ADR-0009: Logging is a platform feature, not a per-call chore

- Status: Accepted
- Date: 2026-07-23

## Context
Agents (and humans) consistently under-log, and then "go check the logs" yields
nothing useful. We also have a hard constraint: this is a bubbletea TUI, and the TUI
**owns the terminal** — a stray `slog.Info` mid-render corrupts the screen. So the
usual "just print structured logs to stderr" does not work here.

## Decision
- **Logs never go to the terminal while the TUI runs.** `slog` (JSON handler) writes to
  a **file**; optionally teed to an in-memory ring buffer the TUI renders in a debug pane.
- **Logging is baked into the primitives.** The supervised worker logs its own
  lifecycle; the pipeline stage-runner logs every stage entry/exit/outcome; the LLM
  client logs every call. Leaf code rarely logs by hand — **nobody can forget to log.**
- **Mandatory run-id correlation.** Every run has an id; every line for that run carries
  it as a structured attr, so concurrent runs don't interleave into mush.
- **Verbose by default (debug on), structured, written for a future agent debugging the
  factory.** Log inputs, decisions, and outcomes at every seam — not just errors.
- **The logger is injected explicitly** (a constructor dependency), not global.

## Rejected alternatives
- **Context-carried logger** (`slog` in `context.Context`): rejected. If someone forgets
  to put the logger in the context, code silently falls back to a no-op logger — no logs,
  no error. That silent-on-forget is exactly the sharp edge we ban.
- **Global `slog.Default()`**: rejected — untraceable and untestable.

## Consequences
Explicit injection is cheap *because* logging lives at the seams: only a handful of
primitives need a logger, not every leaf function. Tests capture logs from a buffer.
