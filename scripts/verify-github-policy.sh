#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --repository OWNER/REPO --app-id ID [--branch BRANCH]" >&2
}

repository=""
app_id=""
branch="main"
gh_bin="${GH_BIN:-gh}"
while (($# > 0)); do
  case "$1" in
    --repository)
      repository="${2:-}"
      shift 2
      ;;
    --app-id)
      app_id="${2:-}"
      shift 2
      ;;
    --branch)
      branch="${2:-}"
      shift 2
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

if [[ ! "$repository" =~ ^[^/]+/[^/]+$ ]] || [[ ! "$app_id" =~ ^[1-9][0-9]*$ ]] || [[ ! "$branch" =~ ^[A-Za-z0-9._/-]+$ ]]; then
  usage
  exit 2
fi

details_dir="$(mktemp -d /tmp/software-factory-rulesets.XXXXXX)"
trap 'rm -rf -- "$details_dir"' EXIT

ruleset_ids=()
while IFS= read -r ruleset_id; do
  ruleset_ids+=("$ruleset_id")
done < <("$gh_bin" api "repos/${repository}/rulesets?includes_parents=true&per_page=100" --jq '.[].id')
if ((${#ruleset_ids[@]} == 0)); then
  echo '[]' | (cd "$(dirname "$0")/.." && go run ./cmd/verify-github-policy --app-id "$app_id" --branch "$branch")
  exit $?
fi

for ruleset_id in "${ruleset_ids[@]}"; do
  "$gh_bin" api "repos/${repository}/rulesets/${ruleset_id}" >"${details_dir}/${ruleset_id}.json"
done

jq -s '.' "${details_dir}"/*.json |
  (cd "$(dirname "$0")/.." && go run ./cmd/verify-github-policy --app-id "$app_id" --branch "$branch")
