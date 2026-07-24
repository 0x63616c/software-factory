# Developer command runner. Thin recipes ONLY — the check list itself lives in
# lefthook.yml (single source, tenet #12). Recipes here delegate; they never re-list
# checks, so they can't drift from the hooks or CI. `just` is not required to build or
# contribute — scripts/setup.sh and `go tool ...` work without it.

# List recipes.
default:
    @just --list

# Fresh-clone bootstrap: install the git-hook wall. Run this first.
setup:
    ./scripts/setup.sh

# Run the full pre-commit wall over the whole tree (build + lint + nolint-ban + test).
check:
    go tool lefthook run pre-commit --all-files

# Run the heavier pre-push wall (race tests + govulncheck).
check-push:
    go tool lefthook run pre-push --all-files

# Unit tests with the race detector.
test:
    go test -race ./...

# Lint via the pinned golangci-lint.
lint:
    go tool golangci-lint run ./...
