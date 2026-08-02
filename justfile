set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

bootstrap:
    ./scripts/bootstrap.sh

# Manually exercise the real Responses boundary. Requires the protected canary env.
canary-responses:
    go run ./cmd/canary-responses

archive-check:
    ./scripts/archive-check.sh

verify: archive-check
    go build ./cmd/...
    go vet ./...
    ./scripts/regenerate.sh
    git diff --exit-code -- internal/api/openapi.yaml internal/store/storedb web/src/api/generated.ts
    go test -race ./...
    golangci-lint run
    golangci-lint run --build-tags=e2e ./internal/e2e/...
    bun run --cwd web typecheck
    bun run --cwd web test
    bun run --cwd web build

integration:
    go test -race -count=1 -tags=integration ./internal/runworkercapability

e2e:
    ./scripts/e2e.sh

scenario ACTION TRACE:
    ./scripts/scenario.sh "{{ACTION}}" "{{TRACE}}"

scenario-replay TRACE:
    ./scripts/scenario.sh replay "{{TRACE}}"

release-check VERSION:
    ./scripts/release-check.sh "{{VERSION}}"

release:
    ./scripts/release-version.sh --create-tag
