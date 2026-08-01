# ADR-0021: Shell-outs are wrapped, argv-only, and context-aware

- Status: Accepted
- Date: 2026-07-23

## Context
The factory shells out to external tools — `git`, `gh`, and whatever a target project's
build needs. External processes are a risky, leaky edge: they're non-deterministic
(untestable if called directly), long-running (must be cancellable), and a
command-injection hazard if arguments are ever interpolated into a shell string.

## Decision
Every external command goes through a narrow wrapper, never `os/exec` scattered inline:

- **Behind an interface** so unit tests inject a fake and never spawn a real process
  ([ADR-0004] / the testability floor). This is the "wrap only the risky/leaky deps"
  case from the dependency stance.
- **Argv-only.** Arguments are passed as an explicit `[]string`, **never** interpolated
  into `sh -c "<string>"`. No shell, no injection surface, no quoting bugs.
- **Context-aware.** The wrapper takes `context.Context` and is cancellable ([ADR-0019]),
  so a shell-out dies with its run.
- **Output and exit codes captured**, and failures wrapped with `cockroachdb/errors`
  ([ADR-0006]) including the command and its context — never swallowed.

## Rejected alternatives
- **`exec` a shell with an interpolated string** (`sh -c "git commit -m "+msg`). Command
  injection, quoting hell, unreadable. Banned outright.
- **Calling `os/exec` directly, scattered across the codebase.** Untestable (spawns real
  processes), uncancellable, and every call site reinvents output/error handling — the
  raw-goroutine mistake ([ADR-0008]) in another guise.
- **Discarding stdout/stderr or the exit code.** Blind to why a git op failed — kills
  operability.

## Consequences
- Unit tests drive git/gh flows against a fake, deterministically, no real repo.
- One place owns process spawning: cancellation, logging (the wrapper logs each command,
  per [ADR-0009]), and error wrapping are consistent.
- The injection surface is zero by construction, not by review vigilance.
