# ADR-0006: Error handling with cockroachdb/errors

- Status: Accepted
- Date: 2026-07-23

## Context
A long-running TUI that ships code to production needs errors that are traceable in logs
(operability), fail loud on broken invariants (correctness), and never take down the
whole factory when one ticket is bad. Go idiom (errors for expected failures, panic for
bugs) needs adapting to a process that must survive overnight.

## Decision
Use `cockroachdb/errors`. Rules:

- **Wrap on the way up, always.** Every error gets `errors.Wrap`/`Wrapf` with context as
  it propagates; a stack is attached once and the chain is annotated. **Bare
  `return err` is banned** — it throws away the trail.
- **Three error kinds, chosen by what the caller does with the error:**
  - *Opaque* (just wrapped) — the default; the caller only bubbles it up.
  - *Sentinel* (`var ErrX = errors.New(...)`, matched with `errors.Is`) — when a caller
    branches on a condition.
  - *Typed* (a struct, matched with `errors.As`) — only when the caller needs *data* out
    of the error. Start opaque; don't reach for sentinel/typed until a caller needs it.
- **Invariant violations are assertions, not errors:** `errors.AssertionFailedf` for
  "this can't happen" — loud, tagged, distinct from expected failure.
- **panic/error policy:**
  - *Startup/config failure* → a **clean, helpful, user-facing message + non-zero exit**,
    never a panic dump. Missing config is a **user error**, not a programmer error
    (see [ADR-0007]).
  - *Operational failure* (LLM call failed, malformed ticket, git op bounced) → a
    wrapped error value, handled and surfaced — never a panic.
  - *Broken invariant mid-run* → halt **that run** (mark it failed), not the process.
  - *Every long-running unit of work has a top-level recover boundary* that logs the full
    chain, marks the unit failed, and keeps the process alive.

> **Panics never escape a single unit of work.** A bad ticket kills its run and is
> visible; it never kills the factory.

## Rejected alternatives
- **Stdlib `errors` only.** Rejected: `cockroachdb/errors` gives stack capture, hints,
  and richer matching that serve operability and legibility.
- **Panic freely / let panics crash the process (TigerStyle-brutal).** Rejected: that is
  affordable for TigerBeetle because deterministic simulation testing (DST) can replay a
  crash. We have no DST, so a brutal crash costs an overnight run with no replay. We take
  the supervised path instead — operability (#3) demands the factory degrade one ticket,
  not die whole.
- **Recover-and-continue on a broken invariant.** Rejected: correctness (#2) beats
  operability (#3) — running on corrupted state produces confident garbage.

## Consequences
- The recover boundary is implemented mechanically by the supervised-worker primitive
  ([ADR-0008]); "recovering from panics" is deliberate here, scoped to one unit of work.
- Silent `recover` and ignored errors are banned by the no-escape-hatches tenet and
  enforced by `errcheck` / the lint stack.
