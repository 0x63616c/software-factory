#!/usr/bin/env bash
set -euo pipefail

readonly archive_source_commit="afd075dfc20edc3e8228b3baba3dd40ffd7110e9"
readonly expected_count="54"

actual_count="$(git ls-files '_archived/**' | wc -l | tr -d ' ')"
if [[ "${actual_count}" != "${expected_count}" ]]; then
  printf 'archive: got %s tracked files, want %s\n' "${actual_count}" "${expected_count}" >&2
  exit 1
fi

while IFS=$'\t' read -r source_metadata source_path; do
  read -r source_mode _ source_object <<<"${source_metadata}"
  archived_path="_archived/${source_path}"
  if [[ ! -e "${archived_path}" && ! -L "${archived_path}" ]]; then
    printf 'archive: missing %s\n' "${archived_path}" >&2
    exit 1
  fi
  if [[ "${source_mode}" == "120000" ]]; then
    archived_object="$(printf '%s' "$(readlink "${archived_path}")" | git hash-object --stdin)"
  else
    archived_object="$(git hash-object "${archived_path}")"
  fi
  if [[ "${source_object}" != "${archived_object}" ]]; then
    printf 'archive: content changed at %s\n' "${archived_path}" >&2
    exit 1
  fi
done < <(git ls-tree -r "${archive_source_commit}")

if git ls-files --others --exclude-standard -- _archived | grep -q .; then
  printf 'archive: unexpected untracked content under _archived\n' >&2
  exit 1
fi

printf 'archive: verified %s byte-identical files from %s\n' \
  "${expected_count}" "${archive_source_commit}"
