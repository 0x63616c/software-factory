#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/release-version.sh VERSION [options]

Generate deterministic release notes from all commits since the last SemVer tag.

Options:
  --notes-file PATH      Write release notes to PATH (default: .artifacts/release-notes/<VERSION>.md)
  --create-tag           Create an annotated git tag for VERSION using the generated notes
  --push-tag             Push the created tag after tagging (implies --create-tag)
  --no-codex             Skip codex summarization and use a compact fallback summary
  -h, --help             Show this help
EOF
}

if [[ ${1-} == -h || ${1-} == --help ]]; then
  usage
  exit 0
fi

version="${1-}"
if [[ -z "${version}" ]]; then
  echo "release version required" >&2
  usage >&2
  exit 1
fi
if [[ ! "${version}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "release version must be a stable SemVer tag such as v0.1.3: ${version}" >&2
  exit 1
fi

notes_file="${notes_file:-}"
create_tag=0
push_tag=0
skip_codex=0
shift

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
    --push-tag)
      create_tag=1
      push_tag=1
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

if ! command -v git >/dev/null 2>&1; then
  echo "release-version requires git" >&2
  exit 1
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "${repo_root}"

mkdir -p "${repo_root}/.artifacts/release-notes"
if [[ -z "${notes_file}" ]]; then
  notes_file="${repo_root}/.artifacts/release-notes/${version}-release-notes.md"
fi

release_target="HEAD"
if git rev-parse --verify "${version}^{tag}" >/dev/null 2>&1; then
  release_target="${version}"
  latest_tag="$(git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --abbrev=0 "${version}^" 2>/dev/null || true)"
else
  latest_tag="$(git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --abbrev=0 2>/dev/null || true)"
fi
if [[ -z "${latest_tag}" ]]; then
  latest_tag="<first release>"
  commit_range="${release_target}"
else
  commit_range="${latest_tag}..${release_target}"
fi

mapfile -t commits < <(
  if [[ "${commit_range}" == "HEAD" ]]; then
    git log --no-color --pretty=format:'%H%x1f%s%x1f%an' HEAD
  else
    git log --no-color --pretty=format:'%H%x1f%s%x1f%an' "$commit_range"
  fi
)

if [[ "${#commits[@]}" -eq 0 ]]; then
  echo "No new commits found in ${commit_range}"
fi

origin_url="$(git config --get remote.origin.url || true)"
commit_base=""
case "${origin_url}" in
  git@github.com:*)
    commit_base="https://github.com/${origin_url#git@github.com:}"
    ;;
  https://github.com/*)
    commit_base="${origin_url}"
    ;;
  *)
    commit_base=""
    ;;
esac
commit_base="${commit_base%.git}"

escape_cell() {
  local value="${1}"
  value="${value//$'\r'/ }"
  value="${value//$'\n'/ }"
  value="${value//|/\\|}"
  printf '%s' "${value}"
}

if (( ${#commits[@]} > 0 )); then
  commit_rows=()
  commit_lines_for_summary=()
  for line in "${commits[@]}"; do
    IFS=$'\x1f' read -r sha subject author <<<"${line}"
    short_sha="${sha:0:8}"
    if [[ -n "${commit_base}" ]]; then
      link="[${short_sha}](${commit_base}/commit/${sha})"
    else
      link="${short_sha}"
    fi
    commit_rows+=("| ${link} | $(escape_cell "${subject}") | $(escape_cell "${author}") |")
    commit_lines_for_summary+=("- ${short_sha}: ${subject}")
  done
else
  commit_rows=("| _No changes found_ | _No changes found_ | _No changes found_ |")
  if [[ "${latest_tag}" == "<first release>" ]]; then
    commit_lines_for_summary=("- No changes found since first release.")
  else
    commit_lines_for_summary=("- No changes found since ${latest_tag}.")
  fi
fi

if (( skip_codex == 0 )) && command -v codex >/dev/null 2>&1; then
  summary_file="$(mktemp)"
  summary_prompt="$(printf '%s\n' "${commit_lines_for_summary[@]}")"
  if codex exec --skip-git-repo-check --output-last-message "${summary_file}" --sandbox workspace-write <<EOF
You are generating release notes for Software Factory.
Write a short, practical summary for "What changed" based on this commit list.
Keep it to at most 6 bullets and no more than 120 words.
Do not include a heading line (no "What changed" title).

${summary_prompt}
EOF
  then
    summary="$(cat "${summary_file}")"
  else
    summary=""
  fi
  rm -f "${summary_file}"
else
  summary=""
fi

if [[ -z "${summary}" ]]; then
  summary="Release contains ${#commits[@]} commit(s) since ${latest_tag}."
  if (( ${#commits[@]} > 0 )); then
    summary="Release includes ${#commits[@]} commit(s) since ${latest_tag}, each documented in the table below."
  fi
fi

{
  printf '# What changed\n'
  printf '\n%s\n\n' "${summary}"
  printf '## Changes since %s\n\n' "${latest_tag}"
  printf '| Commit | Message | Author |\n'
  printf '| --- | --- | --- |\n'
  printf '%s\n' "${commit_rows[@]}"
  printf '\n'
} > "${notes_file}"

echo "release notes written to ${notes_file}"
echo "from ${commit_range}"

if (( create_tag == 1 )); then
  if git rev-parse "${version}" >/dev/null 2>&1; then
    echo "tag ${version} already exists" >&2
    exit 1
  fi
  git tag -a "${version}" -F "${notes_file}"
  echo "created annotated tag ${version}"
fi

if (( push_tag == 1 )); then
  if ! git remote get-url origin >/dev/null 2>&1; then
    echo "origin remote not configured for push" >&2
    exit 1
  fi
  git push origin "${version}"
  echo "pushed tag ${version}"
fi
