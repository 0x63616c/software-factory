# ADR-0013: The enforcement pyramid — don't rely on reading

- Status: Accepted
- Date: 2026-07-23

## Context
Skills and docs get ignored. A real prior attempt — a "writing scalable TypeScript"
skill — was frequently not read by agents, so the guidance simply didn't take. Hoping
newer models "read better" is not a strategy. This is the failure mode the entire
mechanical-correctness thread of SoftwareStyle exists to defeat.

## Decision
**Nothing critical may depend on an agent choosing to read something.** Always climb as
high on this pyramid as the rule allows:

1. **Mechanical (a wall).** Linter / type / codegen / CI check. Survives an unread doc.
   This is why `.golangci.yml` is a first-class artifact: `depguard` (import boundaries),
   `forbidigo` (`os.Getenv` outside config, stdout writes, `//nolint`), `errcheck`
   (no ignored errors), `exhaustive` (enum switches), `staticcheck`.
2. **Always-loaded context.** Non-negotiable tenets go in `AGENTS.md` (always in
   context), never in an on-demand skill.
3. **Harness hooks.** Run lint/tests on edit or block on `Stop` — the harness enforces,
   not the agent's goodwill.
4. **Skills.** For *procedures* only ("add a migration"), never the sole thing between an
   agent and a mistake that matters.

## Rejected alternatives
- **Put the standard in a skill and trust it's read**: rejected — that is precisely the
  documented failure. Suggestions are optional; walls aren't.
- **Nag/remind harder**: rejected — doesn't scale and doesn't guarantee.

## Consequences
Every tenet is triaged onto a rung: tenets → `AGENTS.md` + linters; critical invariants
→ hooks; procedures → skills. golangci-lint cannot hard-ban a well-formed `//nolint`, so
that ban is a CI grep (noted in `.golangci.yml`). This ADR retroactively justifies every
"make it mechanical" call in the other ADRs.
