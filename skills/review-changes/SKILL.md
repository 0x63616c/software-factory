---
name: review-changes
description: Use when reviewing a diff/branch/PR of code going INTO the factory — checks it against SoftwareStyle's judgment-tier tenets (the ones linters can't enforce).
---

# Review changes against SoftwareStyle

Reviews new code into this repo (Layer A) against
[`docs/SoftwareStyle.md`](../../docs/SoftwareStyle.md) and [`docs/adr/`](../../docs/adr/).
Its job is the **judgment-tier** tenets a linter can't enforce — the *review* rung of the
enforcement pyramid ([ADR-0013](../../docs/adr/0013-enforcement-pyramid.md)), above hooks.

**Do not re-review what the wall enforces.** Mechanical tenets are green-or-build-failed;
reviewing them is wasted effort. Review only what the compiler and `.golangci.yml` can't see.

## The wall already covers these — skip
- Import boundaries (`depguard`): engine ⊥ bubbletea, `database/sql` in `store`, rand in `cmd/`.
- Banned calls (`forbidigo`): `os.Getenv` outside `config`, `time.Now`/`time.Local` outside `clock`, stdout.
- `errcheck`, `exhaustive`, `//nolint` (`nolintlint` + CI grep), formatting, doc comments (`revive`/`godot`).

## The judgment-tier checklist — the job
Ask each of the diff; cite the tenet when flagging.

1. **Deep module / narrow door** — did a change *widen* the public surface? A correct split shrinks the door, exports less.
2. **No leaky abstractions** — do `sqlc`/`bubbletea`/third-party types escape their home package?
3. **Legibility** — readable *cold*, without opening five files? Names state intent?
4. **Testability shape** — every new external edge injected? Any real-world touch (real clock/net/LLM/fs, or `sleep`) in a unit test?
5. **Comments & docstrings** — why-not-what; no commented-out code; `TODO(scope):`, never bare.
6. **Test naming (ginkgo)** — present-tense observable behaviour; no "should"/"test"/"correctly".
7. **Construction** ([ADR-0018](../../docs/adr/0018-construction.md)) — required deps positional; optional via functional options over an unexported `options`; validate once in `New`.
8. **Time & units** ([ADR-0016](../../docs/adr/0016-time-and-units.md)) — UTC in engine; `time.Duration` for durations (no `_ms`); unit suffix for untyped numbers; RFC3339 serialized.
9. **Parse, don't validate** — external input becomes a typed domain value at the boundary, not a raw string/map re-checked later.
10. **Illegal states unrepresentable** — typed IDs ([ADR-0017](../../docs/adr/0017-identifiers.md)); no invalid zero value; lifecycle = typed exhaustive state machine with a guarding transition.
11. **Grab-bag packages** — reject `util`/`common`/`helpers`/`misc`/`shared`.
12. **Fail-fast & helpful** ([ADR-0006](../../docs/adr/0006-error-handling.md)) — clean message + non-zero exit, never a panic dump; errors wrapped up; no bare `return err`.
13. **Operability** ([ADR-0009](../../docs/adr/0009-logging-is-a-platform-feature.md)) — logging at the seams, not forgotten in leaf code. Secrets never logged.
14. **ADR conformance** — does it contradict a `docs/adr/` decision? Intentional revisit needs a new/updated ADR; a silent contradiction is the finding.

## Report
One line per finding: `path:line — <tenet at risk> — <fix>`. No praise, no scope creep.
A *mechanical*-tenet miss isn't hand-reviewed — flag it as a **gap in `.golangci.yml`**.

## Verification
Advisory. The wall must also be green independently:
```
golangci-lint run ./...
go test -race ./...
```
