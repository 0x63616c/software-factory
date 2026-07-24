# ADR-0026: Skills have two consumers — canonical at the root, symlinked into `.claude`

- Status: Accepted
- Date: 2026-07-23

## Context
A skill (a procedure like "add a migration") is read by **two** different consumers, and
they look in different places:

1. **The factory engine** reads a target repo's skills as Layer-B *data*, from the
   conventional repo-root `skills/` directory ([ADR-0014]). Since this repo is the
   factory's own first target, our skills live at `skills/<name>/SKILL.md`.
2. **The human's coding agents** (Claude Code) invoke skills, but only discover them
   under `.claude/skills/<name>/SKILL.md` — a repo-root `skills/` is *not* scanned.

So the same four skills need to be visible in two locations without becoming two copies
that drift (tenet #12, single source of truth).

## Decision
Keep the **one canonical copy** at `skills/<name>/` and make it discoverable to Claude
Code with **per-skill directory symlinks**: `.claude/skills/<name>` → `../../skills/<name>`.

- **Per-skill symlinks, not a single `.claude/skills` → `../skills` directory symlink.**
  Claude Code's docs explicitly support a `<name>` *entry* being a symlink to a directory
  elsewhere on disk; a symlinked skills *directory* is not documented. This repo does not
  rely on undocumented behavior ([ADR-0013]), so we use the guaranteed form.
- **The symlinks are committed**, so a fresh clone can invoke the skills with no setup
  step. The command name is the directory (= symlink) name.
- **`scripts/link-skills.sh` regenerates them idempotently**, so adding/removing a skill
  is one command (`just link-skills`), not hand-managed links. `scripts/setup.sh` calls
  it too, so a bootstrapped clone is always consistent.

## Rejected alternatives
- **Copy the skills into `.claude/skills/`** — two copies, guaranteed to drift. The whole
  reason skills live in a conventional file is single-source ([ADR-0014]).
- **Single `.claude/skills` → `../skills` directory symlink** — one link, auto-covers new
  skills, but relies on undocumented directory-level symlink discovery. Rejected per
  ADR-0013 (don't rely on hope); the generated per-skill links cost nothing and are
  documented behavior.
- **Move the canonical skills under `.claude/skills/` and symlink the *other* way** —
  inverts the model: the engine's Layer-B data would then live in a Claude-specific
  directory, coupling the language-agnostic engine to one agent harness. Wrong home.

## Consequences
- Adding a skill: create `skills/<name>/SKILL.md`, run `just link-skills`, commit the new
  symlink. The regenerator also prunes links whose target skill was removed.
- Codex reads `AGENTS.md` natively ([ADR-0014]); it has no known SKILL.md invocation
  mechanism, so no `.codex` equivalent is created. If that changes, it is another
  generated-symlink target off the same canonical `skills/`.
- The two-consumers split is now explicit: `skills/` is the home; `.claude/skills/` is a
  generated view for one consumer.
