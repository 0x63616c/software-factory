# ADR-0025: CI is the unbypassable backstop, delegating to lefthook

- Status: Accepted
- Date: 2026-07-23

## Context
The git-hook wall ([ADR-0013] rung 3) is real but has two holes: hooks live **per-clone**
(a fresh clone has none until `lefthook install` runs) and are **`--no-verify`-skippable**
by any committer. So the mechanical wall is, in practice, opt-in — exactly the "don't
rely on someone choosing to" failure ADR-0013 exists to defeat. This was noted as
deferred; a PR review surfaced it as worth closing now rather than with the runtime spine.

The obvious risk with adding CI is **drift**: a second copy of the check list that
silently diverges from the local hooks, so "green locally" and "green in CI" stop meaning
the same thing.

## Decision
Add GitHub Actions CI (`.github/workflows/ci.yml`) that runs on every push to `main` and
every pull request, where it cannot be turned off. It is the enforcement authority; the
local hooks are the fast-feedback copy.

Drift is designed out rather than hoped away:

- **One check list.** `lefthook.yml` is the single source of truth (tenet #12). CI runs
  the checks by invoking `go tool lefthook run pre-commit --all-files` and
  `... pre-push --all-files` — it does **not** re-list them. Add a check to `lefthook.yml`
  and it runs locally and in CI from the one definition.
- **One set of versions.** Every tool is `go tool <name>`, pinned in go.mod's `tool`
  block (lefthook, golangci-lint, govulncheck). CI and laptops resolve the same versions
  off the same go.mod; there is no global install to skew.
- **The `//nolint` grep ban** moved from a comment in `.golangci.yml` into a real
  `lefthook` command (`nolint-ban`), so it too is shared by both rather than being a
  CI-only step.

A fresh-clone bootstrap (`scripts/setup.sh`, wrapped by `just setup`) installs the local
hooks so the two walls line up from the first commit.

## Rejected alternatives
- **Re-list the checks in the workflow YAML** — the drift trap. Two lists, one truth,
  guaranteed to diverge. Rejected.
- **Composite action / reusable workflow holding the commands** — still a second list to
  keep in sync with the hooks; lefthook already is that list.
- **CI only, drop the git hooks** — loses the fast local signal before a push. Keep both;
  make them the same commands.
- **Keep deferring CI** — leaves the wall opt-in and bypassable, which is the hole.

## Consequences
- CI is one job that shells to lefthook; adding coverage means editing `lefthook.yml`,
  never the workflow. `lefthook run` executes a hook's commands straight from config, so
  CI needs no `lefthook install`.
- The check list is authoritative in one file for humans, agents, local hooks, and CI.
- The `.golangci.yml` footer no longer says "add the grep to CI" — the grep is a lefthook
  command that CI already runs.
- Future heavier gates (integration/e2e tiers, [ADR-0012]) attach as new lefthook
  commands or a new hook stage; CI inherits them without edits here.
