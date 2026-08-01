#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/release-version.sh [VERSION] [options]

Ask Codex to select the next stable SemVer version from Conventional Commits and
write its release notes. Without VERSION, Codex selects the version. With
VERSION, Codex must confirm that the requested version is the appropriate next
release.

Options:
  --notes-file PATH      Write release notes to PATH (default: .artifacts/release-notes/<VERSION>.md)
  --create-tag           Create an annotated git tag using the generated notes
  --no-codex             Use deterministic notes; requires VERSION
  -h, --help             Show this help
EOF
}

if [[ ${1-} == -h || ${1-} == --help ]]; then
  usage
  exit 0
fi

requested_version=""
if [[ ${1-} != --* && -n ${1-} ]]; then
  requested_version="$1"
  shift
fi
if [[ -n "$requested_version" && ! "$requested_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "release version must be a stable SemVer tag such as v0.1.3: ${requested_version}" >&2
  exit 1
fi

notes_file="${notes_file:-}"
create_tag=0
skip_codex=0
while (($#)); do
  case "$1" in
    --notes-file)
      notes_file="${2-}"
      shift 2
      ;;
    --create-tag)
      create_tag=1
      shift
      ;;
    --no-codex)
      skip_codex=1
      shift
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

for command in git jq; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "release-version requires $command" >&2
    exit 1
  fi
done
if (( skip_codex == 1 )) && [[ -z "$requested_version" ]]; then
  echo "--no-codex requires an explicit VERSION" >&2
  exit 1
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
if [[ -n "$(git status --porcelain)" ]]; then
  echo "release-version requires a clean checkout" >&2
  exit 1
fi

origin_url="$(git config --get remote.origin.url || true)"
github_repository="${GITHUB_REPOSITORY:-}"
if [[ -z "$github_repository" ]]; then
  case "$origin_url" in
    git@github.com:*) github_repository="${origin_url#git@github.com:}" ;;
    https://github.com/*) github_repository="${origin_url#https://github.com/}" ;;
  esac
  github_repository="${github_repository%.git}"
fi
if [[ -z "$github_repository" || ! "$github_repository" =~ ^[^/]+/[^/]+$ ]]; then
  echo "release-version requires a GitHub origin or GITHUB_REPOSITORY=owner/repo" >&2
  exit 1
fi

if ! git remote get-url origin >/dev/null 2>&1; then
  echo "release-version requires an origin remote" >&2
  exit 1
fi

git fetch --tags origin

local_tags_output="$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*')"
mapfile -t local_tags < <(printf '%s\n' "$local_tags_output")
remote_tag_refs="$(git ls-remote --tags origin 'v[0-9]*.[0-9]*.[0-9]*')"
mapfile -t remote_tags < <(
  printf '%s\n' "$remote_tag_refs" |
    awk '{sub("refs/tags/", "", $2); sub("\\^\\{\\}$", "", $2); print $2}'
)

gh_bin="${GH_BIN:-gh}"
if (( skip_codex == 0 )) && ! command -v "$gh_bin" >/dev/null 2>&1; then
  echo "release-version requires gh to inspect GitHub Releases" >&2
  exit 1
fi
release_tags=()
if (( skip_codex == 0 )); then
  release_tag_output="$("$gh_bin" api --paginate "repos/${github_repository}/releases?per_page=100" --jq '.[].tag_name')"
  mapfile -t release_tags < <(printf '%s\n' "$release_tag_output")
fi

mapfile -t known_versions < <(
  printf '%s\n' "${local_tags[@]}" "${remote_tags[@]}" "${release_tags[@]}" |
    awk '/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/' |
    sort -u
)

if [[ -n "$requested_version" ]] && (( create_tag == 1 )); then
  for version in "${known_versions[@]}"; do
    if [[ "$version" == "$requested_version" ]]; then
      echo "version ${requested_version} already exists as a tag or GitHub Release" >&2
      exit 1
    fi
  done
fi

release_target="HEAD"
if [[ -n "$requested_version" ]] && git rev-parse --verify "${requested_version}^{tag}" >/dev/null 2>&1; then
  release_target="$requested_version"
  latest_tag="$(git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --abbrev=0 "${requested_version}^" 2>/dev/null || true)"
else
  latest_tag="$(git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --abbrev=0 HEAD 2>/dev/null || true)"
fi
if [[ -z "$latest_tag" ]]; then
  latest_tag="<first release>"
  commit_range="$release_target"
  changes_since="the repository root"
else
  commit_range="${latest_tag}..${release_target}"
  changes_since="$latest_tag"
fi

commits_output="$(git log --no-color --pretty=format:'%H%x1f%s%x1f%an' "$commit_range")"
mapfile -t commits < <(printf '%s\n' "$commits_output")
if [[ "${#commits[@]}" -eq 0 ]]; then
  echo "no commits found since ${changes_since}; refusing to create an empty release" >&2
  exit 1
fi

origin_url="${origin_url%.git}"
commit_base=""
case "$origin_url" in
  git@github.com:*) commit_base="https://github.com/${origin_url#git@github.com:}" ;;
  https://github.com/*) commit_base="$origin_url" ;;
esac

commit_lines=()
commit_rows=()
for line in "${commits[@]}"; do
  IFS=$'\x1f' read -r sha subject author <<<"$line"
  short_sha="${sha:0:8}"
  commit_body="$(git log -1 --format=%B "$sha")"
  commit_lines+=("${short_sha}: ${commit_body}")
  if [[ -n "$commit_base" ]]; then
    link="[${short_sha}](${commit_base}/commit/${sha})"
  else
    link="$short_sha"
  fi
  subject="${subject//$'\r'/ }"
  subject="${subject//$'\n'/ }"
  author="${author//$'\r'/ }"
  author="${author//$'\n'/ }"
  commit_rows+=("| ${link} | ${subject//|/\\|} | ${author//|/\\|} |")
done

decision_file="$(mktemp)"
prompt_file="$(mktemp)"
trap 'rm -f "$decision_file" "$prompt_file"' EXIT

if (( skip_codex == 1 )); then
  version="$requested_version"
  {
    printf '# What changed\n\n'
    printf 'Release includes %s commit(s) since %s.\n\n' "${#commits[@]}" "$changes_since"
    printf '## Changes since %s\n\n' "$changes_since"
    printf '| Commit | Message | Author |\n| --- | --- | --- |\n'
    printf '%s\n' "${commit_rows[@]}"
  } > "$decision_file"
else
  {
    cat "$repo_root/scripts/release-prompt.md"
    printf '\n'
    printf 'Requested version (empty means choose it yourself): %s\n' "$requested_version"
    printf 'Latest reachable SemVer tag: %s\n' "$latest_tag"
    printf 'Known local, remote, and GitHub Release versions: %s\n' "$(printf '%s, ' "${known_versions[@]}")"
    printf 'GitHub Release records: %s\n' "$(printf '%s, ' "${release_tags[@]}")"
    printf 'Changes since: %s\n\n' "$changes_since"
    printf '%s\n' 'Commit subjects:'
    printf -- '- %s\n' "${commit_lines[@]}"
  } > "$prompt_file"

  codex_bin="${CODEX_BIN:-codex}"
  if ! command -v "$codex_bin" >/dev/null 2>&1; then
    echo "release-version requires codex" >&2
    exit 1
  fi
  "$codex_bin" exec --sandbox read-only --output-schema "$repo_root/scripts/release-decision.schema.json" --output-last-message "$decision_file" - < "$prompt_file"
  if ! jq --exit-status -e '.version | strings | test("^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$")' "$decision_file" >/dev/null; then
    echo "codex returned an invalid release version decision" >&2
    exit 1
  fi
  if ! jq --exit-status -e '.releaseNotes | strings | test("^# What changed\\n\\n[\\s\\S]+\\n\\n## Changes since [^\\n]+\\n\\n\\| Commit \\| Message \\| Author \\|\\n\\| --- \\| --- \\| --- \\|[\\s\\S]*")' "$decision_file" >/dev/null; then
    echo "codex release notes do not match the required format" >&2
    exit 1
  fi
  version="$(jq -r '.version' "$decision_file")"
  if [[ -n "$requested_version" && "$version" != "$requested_version" ]]; then
    echo "codex selected ${version}, not requested version ${requested_version}" >&2
    exit 1
  fi
  for known_version in "${known_versions[@]}"; do
    if [[ "$version" == "$known_version" ]] && { [[ -z "$requested_version" ]] || (( create_tag == 1 )); }; then
      echo "codex selected existing version ${version}" >&2
      exit 1
    fi
  done
fi

mkdir -p "$repo_root/.artifacts/release-notes"
if [[ -z "$notes_file" ]]; then
  notes_file="$repo_root/.artifacts/release-notes/${version}-release-notes.md"
fi
if (( skip_codex == 1 )); then
  cp "$decision_file" "$notes_file"
else
  jq -r '.releaseNotes' "$decision_file" > "$notes_file"
fi

echo "release notes written to ${notes_file}"
echo "from ${commit_range}"
echo "selected ${version}"

if (( create_tag == 1 )); then
  if git rev-parse "$version" >/dev/null 2>&1; then
    echo "tag ${version} already exists" >&2
    exit 1
  fi
  git tag -a "$version" --cleanup=verbatim -F "$notes_file"
  echo "created annotated tag ${version}"
fi
