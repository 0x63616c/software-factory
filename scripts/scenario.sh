#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 || "$1" != "replay" ]]; then
  echo "usage: $0 replay <scenario-trace.json>" >&2
  exit 2
fi

trace="$2"
if [[ ! -f "$trace" ]]; then
  echo "scenario trace does not exist: $trace" >&2
  exit 2
fi
trace_dir="$(cd "$(dirname "$trace")" && pwd)"

exec ./scripts/e2e.sh --replay "$trace_dir/$(basename "$trace")"
