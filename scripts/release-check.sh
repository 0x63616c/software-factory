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
  'gh release create' \
  '--notes-file'; do
  rg --fixed-strings --quiet -- "$required" "$workflow"
done
if rg --fixed-strings --quiet -- ':latest' "$workflow"; then
  echo "release workflow must not publish a moving latest tag" >&2
  exit 1
fi

for document in README.md CONTRIBUTING.md SECURITY.md docs/configuration.md docs/releasing.md docs/compatibility.md; do
  test -s "$document"
done
for link in './docs/configuration.md' './docs/releasing.md' './docs/compatibility.md' './CONTRIBUTING.md' './SECURITY.md'; do
  rg --fixed-strings --quiet "$link" README.md
done

release_notes_file="$(mktemp)"
trap 'rm -f "$release_notes_file"' EXIT
./scripts/release-version.sh "$version" --notes-file "$release_notes_file" --no-codex
rg --fixed-strings --quiet '# What changed' "$release_notes_file"
rg --fixed-strings --quiet '## Changes since ' "$release_notes_file"

retired_module='github.com/0x63616c/world-wide-webb/apps/'software-factory
if rg --glob '!_archived/**' --fixed-strings --quiet "$retired_module" .; then
  echo "active release tree still references the retired embedded module path" >&2
  exit 1
fi

artifact_dir=.artifacts/release
mkdir -p "$artifact_dir"
commit="$(git rev-parse HEAD)"
jq \
  --arg version "$version" \
  --arg commit "$commit" \
  --arg source 'https://github.com/0x63616c/software-factory' \
  '{version:$version,commit:$commit,platform:.platform,images:[.images[] | . + {
    repository:("ghcr.io/0x63616c/software-factory-" + .name),
    tags:[$version,("sha-" + $commit)],
    labels:{
      "org.opencontainers.image.version":$version,
      "org.opencontainers.image.revision":$commit,
      "org.opencontainers.image.source":$source
    }
  }]}' release/images.json > "$artifact_dir/plan.json"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$artifact_dir" && sha256sum plan.json > SHA256SUMS && sha256sum -c SHA256SUMS >/dev/null)
else
  (cd "$artifact_dir" && shasum -a 256 plan.json > SHA256SUMS && shasum -a 256 -c SHA256SUMS >/dev/null)
fi

jq --exit-status --arg version "$version" --arg commit "$commit" '
  .version == $version and .commit == $commit and .platform == "linux/amd64" and
  (.images | length) == 7 and
  all(.images[];
    .tags == [$version,("sha-" + $commit)] and
    .labels["org.opencontainers.image.version"] == $version and
    .labels["org.opencontainers.image.revision"] == $commit and
    .labels["org.opencontainers.image.source"] == "https://github.com/0x63616c/software-factory")
' "$artifact_dir/plan.json" >/dev/null

echo "release contract valid for $version: $artifact_dir/plan.json"
