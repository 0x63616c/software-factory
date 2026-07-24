# software-factory

A **software factory**: a TUI that takes in tickets and produces code merged to
production. It is built to operate on *any* codebase — Go, Python, TypeScript,
whatever — and one of those codebases is **itself**.

This repository is currently in its **foundation** phase: before building the factory,
we pinned down *how we build it* — a style guide in the spirit of TigerBeetle's
TigerStyle, tuned for a codebase written and maintained by agents, for agents.

## Start here

| If you want… | Read |
|---|---|
| The values & tenets (the north star) | [`docs/SoftwareStyle.md`](./docs/SoftwareStyle.md) |
| Always-loaded agent context & operating protocol | [`AGENTS.md`](./AGENTS.md) |
| *Why* a specific structural choice was made | [`docs/adr/`](./docs/adr/) |
| How to do a recurring task (add a domain package, a migration, a worker) | [`skills/`](./skills/) |
| The mechanical enforcement (the wall) | [`.golangci.yml`](./.golangci.yml) |

## The one idea

There are two layers of standards, and they are **the same mechanism, different
content**:

- **Layer A** — the factory's own code (this repo's Go). Governed by `SoftwareStyle.md`.
- **Layer B** — the standards the factory applies to whatever it's *building*. Owned by
  each target project, consumed by the factory as data.

The factory engine is **language-agnostic**; all standards are data it reads from a
repo's conventional files (`AGENTS.md`, `skills/`, lint config). Our own standards live
in exactly those files — so **this repo is the factory's first target project**, and we
dogfood the format from day one.

## Priority ordering

> **Legibility > Correctness > Operability > Economy**

Machine performance is not on the list (this is a human-scale, LLM-latency-bound
system). Testability is a floor beneath all of it, never traded. See
[`docs/SoftwareStyle.md`](./docs/SoftwareStyle.md) for what each term means and how the
trades resolve.

## Layout

```
cmd/factory/      the binary + composition root (manual DI wired here)
internal/         domain packages (deep modules, by domain — names TBD)
docs/             SoftwareStyle.md + adr/
skills/           procedures for recurring tasks (canonical home; ADR-0026)
.claude/skills/   generated symlinks so Claude Code can invoke skills/ (ADR-0026)
scripts/          setup.sh — fresh-clone bootstrap; link-skills.sh
justfile          developer command runner (thin wrappers; `just --list`)
.golangci.yml     the mechanical enforcement layer
.github/          CI — the unbypassable wall (ADR-0025)
```

## Setup

Everything is pinned in `go.mod`'s `tool` block, so all you need is the Go toolchain.
After cloning, run the bootstrap **first** — it installs the git-hook wall
([ADR-0013](./docs/adr/0013-enforcement-pyramid.md)), which is otherwise off on a fresh
clone:

```
./scripts/setup.sh      # or: just setup
```

Then `just --list` shows the dev recipes (`just check` runs the same wall the hooks and
CI run). CI ([ADR-0025](./docs/adr/0025-ci-backstop.md)) re-runs that wall on every push
and PR by delegating to the same `lefthook.yml` — so it can't be bypassed and can't
drift from local.
