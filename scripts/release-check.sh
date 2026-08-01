#!/usr/bin/env bash
set -euo pipefail

version="${1#VERSION=}"
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "release version must be a stable SemVer tag such as v0.1.0: $version" >&2
  exit 1
fi

for command in git jq rg; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "release-check requires $command" >&2
    exit 1
  fi
done

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

./scripts/archive-check.sh
./scripts/regenerate.sh
git diff --exit-code -- internal/api/openapi.yaml internal/store/storedb web/src/api/generated.ts

jq --exit-status '
  .platform == "linux/amd64" and
  [.images[].name] == ["worker","run-worker","relay","api","blobs","codec","console"] and
  (.images | length) == 7 and
  ([.images[].name] | unique | length) == 7 and
  ([.images[].dockerfile] | unique | length) == 7
' release/images.json >/dev/null

while IFS= read -r dockerfile; do
  test -f "$dockerfile"
done < <(jq -r '.images[].dockerfile' release/images.json)

workflow=.github/workflows/release.yml
for required in \
  'tags:' \
  'just release-check' \
  'just verify' \
  'just integration' \
  'just e2e' \
  'ghcr.io/0x63616c/software-factory-' \
  'org.opencontainers.image.version' \
  'org.opencontainers.image.revision' \
  'org.opencontainers.image.source' \
  'sha-${{ github.sha }}' \
  'SHA256SUMS' \
  'gh release create'; do
  rg --fixed-strings --quiet "$required" "$workflow"
done
if rg --fixed-strings --quiet ':latest' "$workflow"; then
  echo "release workflow must not publish a moving latest tag" >&2
  exit 1
fi

for document in README.md CONTRIBUTING.md SECURITY.md docs/configuration.md docs/releasing.md docs/compatibility.md; do
  test -s "$document"
done
for link in './docs/configuration.md' './docs/releasing.md' './docs/compatibility.md' './CONTRIBUTING.md' './SECURITY.md'; do
  rg --fixed-strings --quiet "$link" README.md
done

retired_module='github.com/0x63616c/world-wide-webb/apps/'software-factory
if rg --glob '!_archived/**' --fixed-strings --quiet "$retired_module" .; then
  echo "active release tree still references the retired embedded module path" >&2
  exit 1
fi

echo "release contract valid for $version: 7 immutable linux/amd64 images"
