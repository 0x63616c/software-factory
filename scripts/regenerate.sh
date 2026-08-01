#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
sqlc generate
go run ./cmd/api openapi > internal/api/openapi.yaml
(cd web && bun run generate:api)
# Run from web/ so bunx resolves the package-local pinned Biome version.
(cd web && bunx biome check --write orval.config.js src/api/generated.ts)
