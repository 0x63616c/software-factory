#!/usr/bin/env bash
set -euo pipefail

: "${SOFTWARE_FACTORY_CANARY_REPOSITORY:?set to the disposable owner/repository}"
: "${GH_TOKEN:?set to a token scoped only to the disposable canary repository}"

repository="$SOFTWARE_FACTORY_CANARY_REPOSITORY"
suffix="${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
branch="software-factory-canary/$suffix"
path="software-factory-canary/$suffix.txt"

cleanup() {
  gh api --method DELETE "repos/$repository/git/refs/heads/$branch" >/dev/null 2>&1 || true
}
trap cleanup EXIT

default_branch="$(gh api "repos/$repository" --jq .default_branch)"
base_sha="$(gh api "repos/$repository/git/ref/heads/$default_branch" --jq .object.sha)"
gh api --method POST "repos/$repository/git/refs" \
  -f ref="refs/heads/$branch" \
  -f sha="$base_sha" >/dev/null

content="$(printf 'software-factory exact-head merge canary %s\n' "$suffix" | base64 | tr -d '\n')"
head_sha="$(gh api --method PUT "repos/$repository/contents/$path" \
  -f message="test: exact-head squash merge canary $suffix" \
  -f content="$content" \
  -f branch="$branch" \
  --jq .commit.sha)"

pull_number="$(gh api --method POST "repos/$repository/pulls" \
  -f title="Software Factory exact-head squash merge canary $suffix" \
  -f head="$branch" \
  -f base="$default_branch" \
  -f body='Automated, protected, opt-in canary. The branch is deleted after the check.' \
  --jq .number)"

merge_result="$(gh api --method PUT "repos/$repository/pulls/$pull_number/merge" \
  -f merge_method=squash \
  -f sha="$head_sha")"
jq --exit-status --arg head "$head_sha" \
  '.merged == true and (.sha | type == "string" and length == 40) and $head != ""' \
  <<<"$merge_result" >/dev/null

echo "GitHub canary exact-head squash-merged pull request #$pull_number"
