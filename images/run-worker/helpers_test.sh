#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
cred_dir="$root/credential"
mkdir -p "$cred_dir"

printf '%s' 'bot[bot]' >"$cred_dir/login"
printf '%s' 'first-token' >"$cred_dir/token"

first="$(printf 'protocol=https\nhost=github.com\n\n' | \
  RUN_WORKER_GITHUB_CREDENTIAL_DIR="$cred_dir" ./bin/git-credential-projected get)"
[[ "$first" == *'username=bot[bot]'* ]]
[[ "$first" == *'password=first-token'* ]]

printf '%s' 'second-token' >"$cred_dir/token"
second="$(printf 'protocol=https\nhost=github.com\n\n' | \
  RUN_WORKER_GITHUB_CREDENTIAL_DIR="$cred_dir" ./bin/git-credential-projected get)"
[[ "$second" == *'password=second-token'* ]]
[[ "$second" != *'first-token'* ]]

refused="$(printf 'protocol=https\nhost=attacker.example\n\n' | \
  RUN_WORKER_GITHUB_CREDENTIAL_DIR="$cred_dir" ./bin/git-credential-projected get)"
[[ -z "$refused" ]]

refused="$(printf 'protocol=http\nhost=github.com\n\n' | \
  RUN_WORKER_GITHUB_CREDENTIAL_DIR="$cred_dir" ./bin/git-credential-projected get)"
[[ -z "$refused" ]]

cat >"$root/gh-real" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${GH_TOKEN:-}" == 'second-token' ]]
for arg in "$@"; do
  [[ "$arg" != *'second-token'* ]]
done
EOF
chmod 0755 "$root/gh-real"
RUN_WORKER_GITHUB_CREDENTIAL_DIR="$cred_dir" GH_REAL="$root/gh-real" ./bin/gh auth status
