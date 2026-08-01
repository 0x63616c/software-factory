# ADR-0014: Standards live in the files the factory will read

- Status: Accepted
- Date: 2026-07-23

## Context
Per ADR-0003, the factory consumes a target project's standards as *data* (an
`AGENTS.md` + `skills/` + lint config it reads from the repo) and feeds them to its
agents. Our own repo has standards too. One day the factory will operate on its own
code. Where should our standards physically live?

## Decision
- **Option A: our standards live in the conventional files the factory will naturally
  read** — `AGENTS.md` at the root, a `skills/` folder, `.golangci.yml` at the root.
  Nothing bespoke. The day the factory points at this repo, **the repo is already a
  valid target** — it reads us like any other project, zero rework.
- **`AGENTS.md` is canonical and thin** (always-loaded: tenets + pointers), pushing
  detail into `docs/SoftwareStyle.md`, `docs/adr/`, and on-demand skills to respect the
  Economy axis (it's in context every turn).
- **`CLAUDE.md` is a one-line pointer (`@AGENTS.md`)** so guidance never drifts between
  tools.

## Rejected alternatives
- **A bespoke docs format** (e.g. one big `docs/how-we-code.md`): fine for humans now,
  but would need reshaping later when the factory expects `AGENTS.md` + `skills/`.
- **Maintaining `CLAUDE.md` and `AGENTS.md` separately by hand**: they *will* drift — a
  legibility/correctness rot. Single source of truth instead.
- **Designing a heavyweight bundle schema now**: gold-plating a format before the engine
  exists. We commit to the *principle* (standards are a loadable bundle in conventional
  files), not a schema.

## Consequences
We dogfood the standards-as-data format from day one; self-hosting is nearly free. Layer
A and Layer B share one mechanism (ADR-0003).
