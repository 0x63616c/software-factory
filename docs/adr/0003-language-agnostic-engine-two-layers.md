# ADR-0003: Language-agnostic engine and the two-layer standards model

- Status: Accepted
- Date: 2026-07-23

## Context
Early in design we caught a conflation: "the standards for the software factory" could
mean two different things. The factory is a Go program, but the projects it *builds* may
be Python, TypeScript, Rust, ROS — anything. We had to separate what we're standardizing
now from what the factory produces, especially because the factory will very likely be
pointed at its own repository (self-hosting).

## Decision
Two layers, one mechanism:

- **Layer A — the factory's own code.** How we write the Go that *is* the factory. This
  is what SoftwareStyle and these ADRs define now.
- **Layer B — the standards the factory applies to whatever it's building.** Owned by
  each target project, not by us. Unknown, per-target, pluggable *data*.

**These are the same mechanism, different content.** The factory consumes a target's
standards as data — an `AGENTS.md` + `skills/` + lint config it reads from the repo — and
feeds them to its agents. Our own standards live in exactly those files. Therefore **this
repo is the factory's first target project**: we dogfood the format from day one and
self-hosting is nearly free.

The load-bearing constraint that falls out:

> **The factory engine is language-agnostic. All language- and standards-specific
> knowledge lives in data (the standards bundle), never baked into the engine's code.**

## Rejected alternatives
- **Design what the factory outputs / bake in a target language.** Rejected: we don't
  know the targets, and hardcoding "Go" would make the engine unable to build a Python
  repo — and unable to cleanly self-host.
- **Two deliberately separate formats for Layer A and Layer B.** Rejected: they share a
  mechanism, so separate formats would mean maintaining two things that must stay in
  sync, and would forfeit the "repo is its own first target" property.
- **Design the Layer B bundle format elaborately up front.** Deferred, not adopted:
  we commit to keeping our standards in the *conventional* discoverable files
  (`AGENTS.md`, `skills/`, `.golangci.yml`) so the factory reads us like any other repo,
  without gold-plating a schema before the engine exists.

## Consequences
- The engine↔TUI seam ([ADR-0011]) and every domain package must stay free of
  target-language assumptions.
- Standards are authored as a loadable bundle in standard locations; correctness of
  "load a target's standards" is testable against our own repo.
- Relates to the priority ordering's scoping note ([ADR-0001]): factory *correctness*
  is about the program behaving, never about the quality of emitted code (that's Layer B).
