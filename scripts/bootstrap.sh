#!/usr/bin/env bash
set -euo pipefail

for command in bun docker git go golangci-lint jq rg sqlc; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "bootstrap requires $command" >&2
    exit 1
  fi
done

go_version="$(go env GOVERSION)"
bun_version="$(bun --version)"
sqlc_version="$(sqlc version)"
golangci_version="$(golangci-lint --version)"

[[ "$go_version" == "go1.26.5" ]] || { echo "want Go 1.26.5, found $go_version" >&2; exit 1; }
[[ "$bun_version" == "1.2.19" ]] || { echo "want Bun 1.2.19, found $bun_version" >&2; exit 1; }
[[ "$sqlc_version" == "v1.31.1" ]] || { echo "want sqlc 1.31.1, found $sqlc_version" >&2; exit 1; }
[[ "$golangci_version" == *"version 2.12.2 "* ]] || { echo "want golangci-lint 2.12.2, found: $golangci_version" >&2; exit 1; }

echo "toolchain verified: Go 1.26.5, Bun 1.2.19, sqlc 1.31.1, golangci-lint 2.12.2"
docker --version
git --version
jq --version
rg --version | head -n 1
