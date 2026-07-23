---
name: review-changes
description: Use when reviewing a diff/branch/PR of code going INTO the factory — checks it against SoftwareStyle's judgment-tier tenets (the ones linters can't enforce).
---

# Review changes against SoftwareStyle

Reviews new code going *into* this repo (Layer A) against
[`docs/SoftwareStyle.md`](../../docs/SoftwareStyle.md) and the decisions in
[`docs/adr/`](../../docs/adr/). Its whole job is the **judgment-tier** tenets — the
ones a linter cannot enforce. It is the *review* rung of the enforcement pyramid
([ADR-0013](../../docs/adr/0013-enforcement-pyramid.md)), above hooks.

**Key rule: do not re-review what the wall already enforces.** The mechanical tenets
are green-or-the-build-failed. Spending review effort on them is wasted. Review only
what the compiler and `.golangci.yml` *can't* see.

## What the linters already enforce — do NOT re-review
Skip these; if they were violated the build is already red:
- Import boundaries (`depguard`): engine ⊥ bubbletea/lipgloss, `database/sql` sealed in `store`, `crypto/rand`/`math/rand` only in `cmd/`.
- Banned calls (`forbidigo`): `os.Getenv` outside `config`, `time.Now`/`time.Local` outside `clock`, stdout writes.
- Ignored errors (`errcheck`), non-exhaustive enum switches (`exhaustive`), `//nolint` (`nolintlint` + CI grep).
- Formatting (`gofumpt`), exported-symbol doc comments + package comments (`revive`).

## The judgment-tier checklist — this is the job
Ask each question of the diff. Cite the tenet when you flag something.

1. **Deep module / narrow door** (SoftwareStyle: Deep modules, narrow door) — did this *widen* a public surface that should stay narrow? Did a split export *more* instead of less? A correct split shrinks the door.
2. **No leaky abstractions** (SoftwareStyle: No leaky abstractions) — do `sqlc`/`bubbletea`/third-party types escape their home package into the wider codebase?
3. **Legibility** (the top-ranked value) — can each new file be understood *cold*, without opening five others? Do names state intent?
4. **Testability shape** (SoftwareStyle: Testability floor) — is every new external edge injected? Any real-world touch in a unit test (real clock/network/LLM/filesystem, or a `sleep`)? Tests must be deterministic.
5. **Comments & docstrings** — exported symbols documented starting with their name; comments explain **why, not what**; no commented-out code; `TODO(scope):` format, never a bare TODO.
6. **Test naming (ginkgo)** — `Describe` = unit under test, `Context` = "when …", `It` = present-tense observable behaviour. No "should", "test", or "correctly".
7. **Construction** ([ADR-0018](../../docs/adr/0018-construction.md)) — required deps are positional args; optional config uses functional options over an *unexported* `options` struct; validation happens **once** in `New` (fail-fast), not per-option.
8. **Time & units** ([ADR-0016](../../docs/adr/0016-time-and-units.md)) — UTC only in the engine; `time.Duration` for durations (**no** `_ms` suffix); unit suffix for *untyped* numbers (`sizeBytes`, `maxAgeSeconds`); RFC3339 for serialized times.
9. **Parse, don't validate** — external input (DB rows, API, ticket bodies, user input) is turned into a *typed domain value at the boundary*, not passed around as raw strings/maps and re-checked later.
10. **Illegal states unrepresentable** — typed IDs (not bare strings, [ADR-0017](../../docs/adr/0017-identifiers.md)); no usable-but-invalid zero value (construct via `New`); lifecycle modelled as a typed, exhaustive state machine with a transition function that rejects illegal moves.
11. **Grab-bag packages** — reject `util`/`common`/`helpers`/`misc`/`shared`. Name a package for its one job.
12. **Fail-fast & helpful** ([ADR-0006](../../docs/adr/0006-error-handling.md)) — config/startup failures are clean messages + non-zero exit, never a panic dump; errors wrapped with context on the way up; no bare `return err`.
13. **Operability** ([ADR-0009](../../docs/adr/0009-logging-is-a-platform-feature.md)) — is logging left to the seams/platform (not forgotten in leaf code)? Are secrets ever logged? (They must never be.)
14. **ADR conformance** — does the change contradict a decision in `docs/adr/`? If it *intentionally* revisits one, is there a new or updated ADR? A silent contradiction is the finding.

## How to report
One line per finding: `path:line — <tenet at risk> — <concrete fix>`. No praise, no
scope creep. If a finding is actually a *mechanical*-tenet miss, don't hand-review it —
flag it as a **gap in `.golangci.yml`** (the wall should have caught it) and move on.

## Verification
This review is advisory. The mechanical wall must **also** be independently green:
```
golangci-lint run ./...
go test -race ./...
```
Review does not replace the wall; it covers what the wall can't.
