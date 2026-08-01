---
name: add-domain-package
description: Use when adding a new domain (deep module) under internal/ — establishes the narrow-door shape, naming, and where tests and sub-packages go.
---

# Adding a domain package

A domain package is a **deep module**: narrow public door, deep private room. Follow this
so every domain reads the same way.

## Steps

1. **Name it for its one job.** Can't name the single responsibility in one sentence? Not
   a domain yet — reconsider. Pick the noun the domain *is*, not a layer (`billing`, not
   `models`).
2. **Create `internal/<name>/<name>.go` — the door.** Exported types + the small set of
   functions the rest of the app needs. Exporting a lot means the seam is wrong.
3. **Everything else is the room.** Unexported types, helpers, logic in the same package
   (or private sub-packages, step 6). Outside depends only on the door.
4. **Inject deps via the constructor.** No globals, no `init()`. Constructor takes deps
   (store, clock, logger, llm) as interface args so tests pass fakes ([ADR-0004]). Wire
   the real graph only in `cmd/factory/main.go`.
5. **Tests beside the code** — `<name>_test.go`, ginkgo + gomega, no real world
   (`:memory:` + fakes). Engine/domain code is **test-first**.
6. **Split only when a tell fires** (can't name its one job / you grep to find things /
   past ~7–10 files). Push a *sub-capability* into a **nested `internal/`**
   (`internal/<name>/internal/<subcap>/`) — compiler-sealed to `<name>`. Split by
   sub-capability, never by layer. Splitting that forces more exports is wrong.

## Do not
- Import `bubbletea`/`lipgloss` (depguard fails — engine is headless).
- Import `database/sql` (depguard fails — SQL sealed in `store`).
- Call `os.Getenv` (only in `config`).
- Add `pkg/`. A real micro-library graduates to its own module.
