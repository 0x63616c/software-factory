#!/usr/bin/env bash
# Fresh-clone bootstrap. The FIRST thing to run after cloning.
#
# Git hooks are per-clone: a new clone has zero hooks until they're installed, so the
# lefthook wall (ADR-0013) is OFF until this runs. This script installs them. It needs
# nothing but the Go toolchain — lefthook itself is pinned in go.mod's `tool` block, so
# there is no separate tool to install first.
#
# Idempotent: safe to re-run. `just setup` is a thin wrapper over this.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
	echo "error: Go toolchain not found on PATH — install Go first (see go.mod for the version)." >&2
	exit 1
fi

echo "==> installing git hooks (lefthook, pinned via go.mod)"
go tool lefthook install

echo "==> linking skills for Claude Code discovery"
./scripts/link-skills.sh

echo "==> done. The commit/push wall is now active. Try: just check"
