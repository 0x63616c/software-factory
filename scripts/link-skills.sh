#!/usr/bin/env bash
# (Re)generate the .claude/skills/ symlinks so Claude Code can invoke the repo's
# canonical skills (ADR-0026). The skills themselves live once at the repo-root
# `skills/` (Layer-B data the factory reads); Claude Code only discovers skills under
# `.claude/skills/<name>/SKILL.md`, and it follows per-skill-directory symlinks. So each
# `.claude/skills/<name>` is a symlink to `../../skills/<name>` — single source, two
# consumers, no copies.
#
# Idempotent (ln -sfn). Run after adding or removing a skill; `just link-skills` and
# scripts/setup.sh both call this. The symlinks are committed, so a fresh clone already
# has them without running anything.
set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p .claude/skills

# Drop stale links whose target skill no longer exists.
for link in .claude/skills/*; do
	[ -L "$link" ] || continue
	[ -e "$link" ] || rm -f "$link"
done

for dir in skills/*/; do
	name="$(basename "$dir")"
	ln -sfn "../../skills/$name" ".claude/skills/$name"
done

echo "linked skills: $(ls .claude/skills | tr '\n' ' ')"
