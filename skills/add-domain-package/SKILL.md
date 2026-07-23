---
name: add-domain-package
description: Use when adding a new domain (deep module) under internal/ — establishes the narrow-door shape, naming, and where tests and sub-packages go.
---

# Adding a domain package

A domain package is a **deep module**: a narrow public door, a deep private room
(SoftwareStyle: deep modules, narrow door). Follow this so every domain reads the same way.

## Steps

1. **Name it for its one job.** If you can't name the package's single responsibility
   in one sentence, it's not a domain yet — stop and reconsider. Names are not yet
   fixed globally; pick the noun the domain *is*, not a layer (`billing`, not `models`).

2. **Create `internal/<name>/<name>.go`** — this is the **door**. Put the exported
   types and the small set of exported functions/methods the rest of the app needs
   here. Keep the surface tiny; if you're exporting a lot, the seam is wrong.

3. **Everything else is the room.** Unexported types, helpers, and logic live in the
   same package (or private sub-packages, step 6). The outside world depends only on
   the door.

4. **Inject dependencies via the constructor.** No globals, no `init()` wiring. The
   constructor takes its dependencies (store, clock, logger, llm) as interface
   arguments so a test can pass fakes (SoftwareStyle: Testability floor; ADR-0004 manual constructor
   injection). Wire the real graph only in `cmd/factory/main.go`.

5. **Write tests beside the code** — `<name>_test.go` in the same directory, ginkgo +
   gomega. Unit tests touch no real world; use `:memory:` sqlite and fakes. For
   engine/domain code this is **test-first** (ADR: agent operating protocol).

6. **Split only when a tell fires** (SoftwareStyle: deep modules, narrow door): you can't name its one job / you
   grep to find things / it's past ~7–10 files. Then push a *sub-capability* down into
   a **nested `internal/`** — `internal/<name>/internal/<subcap>/` — so it's
   compiler-sealed and only `<name>` can import it. Split by sub-capability, never by
   layer. If splitting forces you to export more, you split wrong.

## Do not
- Import `bubbletea`/`lipgloss` (depguard will fail the build — the engine is headless).
- Import `database/sql` (depguard will fail — SQL is sealed in `internal/store`).
- Call `os.Getenv` (banned outside `internal/config`).
- Add a `pkg/` package. A genuine reusable micro-library graduates to its own module.
