set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

bootstrap:
    go version
    bun --version
    sqlc version
    golangci-lint --version

archive-check:
    ./scripts/archive-check.sh

verify: archive-check
    go build ./cmd/...
    go vet ./...
    ./scripts/regenerate.sh
    git diff --exit-code -- internal/api/openapi.yaml internal/store/storedb web/src/api/generated.ts
    go test -race ./...
    golangci-lint run
    bun run --cwd web typecheck
    bun run --cwd web test
    bun run --cwd web build

integration:
    go test -race -count=1 -tags=integration ./internal/runworkercapability

e2e:
    ./scripts/e2e.sh

release-check VERSION:
    ./scripts/release-check.sh "{{VERSION}}"
