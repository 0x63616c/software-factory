#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: configure-github-policy.sh --repository OWNER/REPO \
  --approval-ruleset-id ID --app-id ID --user-id ID [--branch BRANCH] [--apply]

Dry-run is the default. --apply creates the non-bypassable required-check
ruleset first, verifies its response, then adds the App's pull-request-only
bypass to the existing approval ruleset.
EOF
}

repository=""
approval_ruleset_id=""
app_id=""
user_id=""
branch="main"
apply=false
checks_name="software-factory-required-checks"
required_check="test-software-factory"
gh_bin="${GH_BIN:-gh}"

while (($# > 0)); do
  case "$1" in
    --repository) repository="${2:-}"; shift 2 ;;
    --approval-ruleset-id) approval_ruleset_id="${2:-}"; shift 2 ;;
    --app-id) app_id="${2:-}"; shift 2 ;;
    --user-id) user_id="${2:-}"; shift 2 ;;
    --branch) branch="${2:-}"; shift 2 ;;
    --apply) apply=true; shift ;;
    *) usage; exit 2 ;;
  esac
done

if [[ ! "$repository" =~ ^[^/]+/[^/]+$ ]] ||
  [[ ! "$approval_ruleset_id" =~ ^[1-9][0-9]*$ ]] ||
  [[ ! "$app_id" =~ ^[1-9][0-9]*$ ]] ||
  [[ ! "$user_id" =~ ^[1-9][0-9]*$ ]] ||
  [[ ! "$branch" =~ ^[A-Za-z0-9._/-]+$ ]]; then
  usage
  exit 2
fi

policy_dir="$(mktemp -d /tmp/software-factory-policy.XXXXXX)"
trap 'rm -rf -- "$policy_dir"' EXIT
approval_current="${policy_dir}/approval-current.json"
approval_target="${policy_dir}/approval-target.json"
checks_target="${policy_dir}/checks-target.json"
rulesets="${policy_dir}/rulesets.json"

api() {
  "$gh_bin" api -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28" "$@"
}

api "repos/${repository}/rulesets/${approval_ruleset_id}" >"$approval_current"
api "repos/${repository}/rulesets?includes_parents=false&per_page=100" >"$rulesets"

if ! jq -e \
  --argjson id "$approval_ruleset_id" \
  --arg branch "refs/heads/${branch}" '
    .id == $id and
    .name == "main-require-codeowner-approval" and
    .target == "branch" and
    .enforcement == "active" and
    .conditions.ref_name.include == [$branch] and
    .conditions.ref_name.exclude == [] and
    (.rules | length) == 1 and
    .rules[0].type == "pull_request" and
    .rules[0].parameters.required_approving_review_count > 0 and
    .rules[0].parameters.require_code_owner_review == true
  ' "$approval_current" >/dev/null; then
  echo "approval ruleset precondition failed: refusing to replace an unexpected rule, target, or enforcement shape" >&2
  exit 1
fi

user_bypass="$(jq -cn --argjson user "$user_id" '[{actor_id:$user,actor_type:"User",bypass_mode:"always"}]')"
target_bypass="$(jq -cn --argjson user "$user_id" --argjson app "$app_id" '[
  {actor_id:$user,actor_type:"User",bypass_mode:"always"},
  {actor_id:$app,actor_type:"Integration",bypass_mode:"pull_request"}
] | sort_by(.actor_id, .actor_type, .bypass_mode)')"
current_bypass="$(jq -c '[.bypass_actors[] | {actor_id,actor_type,bypass_mode}] | sort_by(.actor_id, .actor_type, .bypass_mode)' "$approval_current")"
approval_operation=""
if [[ "$current_bypass" == "$user_bypass" ]]; then
  approval_operation="add_app_approval_bypass"
elif [[ "$current_bypass" == "$target_bypass" ]]; then
  approval_operation="approval_bypass_already_current"
else
  echo "approval ruleset precondition failed: bypass actors drifted from the reviewed current or target set" >&2
  exit 1
fi

jq --argjson app "$app_id" '
  {
    name,
    target,
    enforcement,
    bypass_actors: (([.bypass_actors[] | {actor_id,actor_type,bypass_mode}] + [
      {actor_id:$app,actor_type:"Integration",bypass_mode:"pull_request"}
    ]) | unique_by(.actor_id, .actor_type, .bypass_mode) | sort_by(.actor_id, .actor_type, .bypass_mode)),
    conditions,
    rules
  }
' "$approval_current" >"$approval_target"

jq -n \
  --arg name "$checks_name" \
  --arg branch "refs/heads/${branch}" \
  --arg check "$required_check" '
  {
    name:$name,
    target:"branch",
    enforcement:"active",
    bypass_actors:[],
    conditions:{ref_name:{include:[$branch],exclude:[]}},
    rules:[{
      type:"required_status_checks",
      parameters:{
        do_not_enforce_on_create:false,
        required_status_checks:[{context:$check}],
        strict_required_status_checks_policy:false
      }
    }]
  }
' >"$checks_target"

checks_ids=()
while IFS= read -r checks_id; do
  checks_ids+=("$checks_id")
done < <(jq -r --arg name "$checks_name" '.[] | select(.name == $name) | .id' "$rulesets")
if ((${#checks_ids[@]} > 1)); then
  echo "checks ruleset precondition failed: found duplicate ${checks_name} rulesets" >&2
  exit 1
fi

checks_operation=""
checks_id=""
if ((${#checks_ids[@]} == 0)); then
  checks_operation="create_checks_ruleset"
else
  checks_id="${checks_ids[0]}"
  checks_current="${policy_dir}/checks-current.json"
  api "repos/${repository}/rulesets/${checks_id}" >"$checks_current"
  if ! jq -e --slurpfile target "$checks_target" '
    {name,target,enforcement,bypass_actors,conditions,rules} == $target[0]
  ' "$checks_current" >/dev/null; then
    echo "checks ruleset precondition failed: existing ${checks_name} differs from the reviewed target" >&2
    exit 1
  fi
  checks_operation="checks_ruleset_already_current"
fi

mode="dry-run"
if $apply; then
  mode="apply"
fi
jq -n \
  --arg mode "$mode" \
  --arg repository "$repository" \
  --arg branch "$branch" \
  --arg checks "$checks_operation" \
  --arg approval "$approval_operation" \
  --slurpfile checksPayload "$checks_target" \
  --slurpfile approvalPayload "$approval_target" '
  {
    version:1,
    mode:$mode,
    repository:$repository,
    branch:$branch,
    operations:[
      {kind:$checks,payload:$checksPayload[0]},
      {kind:$approval,payload:$approvalPayload[0]}
    ]
  }
'

if ! $apply; then
  exit 0
fi

# Safe ordering is load-bearing: enforcing checks first can only make merging
# stricter. Adding the App bypass first would create a window with no retained
# required-check boundary.
if [[ "$checks_operation" == "create_checks_ruleset" ]]; then
  checks_response="${policy_dir}/checks-response.json"
  api --method POST "repos/${repository}/rulesets" --input "$checks_target" >"$checks_response"
  if ! jq -e --slurpfile target "$checks_target" '
    {name,target,enforcement,bypass_actors,conditions,rules} == $target[0]
  ' "$checks_response" >/dev/null; then
    echo "created checks ruleset did not match the reviewed target; approval bypass was not changed" >&2
    exit 1
  fi
fi

if [[ "$approval_operation" == "add_app_approval_bypass" ]]; then
  approval_response="${policy_dir}/approval-response.json"
  api --method PUT "repos/${repository}/rulesets/${approval_ruleset_id}" --input "$approval_target" >"$approval_response"
  if ! jq -e --slurpfile target "$approval_target" '
    {name,target,enforcement,bypass_actors,conditions,rules} == $target[0]
  ' "$approval_response" >/dev/null; then
    echo "updated approval ruleset did not match the reviewed target" >&2
    exit 1
  fi
fi
