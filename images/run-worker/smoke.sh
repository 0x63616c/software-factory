#!/usr/bin/env bash
# Verifies the target image's toolchain, workspace, and credential-reader
# contracts against the built image rather than its Dockerfile.
set -euo pipefail

cd "$(dirname "$0")"
img="${1:-sf-run-worker:local}"

# /work is mounted like the Kubernetes emptyDir: root-owned, group-writable,
# and setgid for the pod's fsGroup. This catches image contracts that only fail
# after the image's baked-in /work is masked in a real pod.
run=(docker run --rm --platform linux/amd64 --tmpfs "/work:uid=0,gid=1000,mode=2775")

fail=0
check() { # check <name> <expected-exit> <cmd...>
  local name="$1" want="$2"
  shift 2
  local out got
  set +e
  out="$("${run[@]}" "$img" "$@" 2>&1)"
  got=$?
  set -e
  if [ "$got" != "$want" ]; then
    printf 'FAIL %s: exit %s, want %s\n%s\n' "$name" "$got" "$want" "$out" >&2
    fail=1
    return
  fi
  printf 'ok   %s\n' "$name"
  if [ -n "$out" ]; then
    printf '     %s\n' "$out"
  fi
}

check "every required binary is on PATH" 0 /usr/bin/env sh -c '
  for binary in tar test cat git bun bunx node go run-worker tool-worker git-credential-projected gh; do
    command -v "$binary" >/dev/null || { echo "missing: $binary"; exit 1; }
  done'

check "tar is GNU tar" 0 /usr/bin/env sh -c 'tar --version | head -1 | grep -q "GNU tar"'

check "runs as the shared Run Worker uid and gid" 0 /usr/bin/env sh -c \
  '[ "$(id -u)" = 1000 ] && [ "$(id -g)" = 1000 ]'

check "the workspace is writable under the Kubernetes-shaped mount" 0 /usr/bin/env sh -c \
  '[ "$(pwd)" = /work ] && touch ./cwd-probe && mkdir -p /work/repo && touch /work/repo/probe'

check "required tool versions are available" 0 /usr/bin/env sh -c '
  bun_version="$(bun --version)" && test -n "$bun_version" &&
    bunx_version="$(bunx --version)" && test -n "$bunx_version" &&
    node_version="$(node --version)" && test -n "$node_version" &&
    go_version="$(go version)" && test -n "$go_version" &&
    git_version="$(git --version)" && test -n "$git_version"'

check "Playwright Chromium writes a headless screenshot" 0 /usr/bin/env sh -c '
  screenshot=/tmp/run-worker-playwright-smoke.png
  rm -f "$screenshot"
  test "$PLAYWRIGHT_BROWSERS_PATH" = /ms-playwright
  bunx --yes playwright@1.60.0 screenshot \
    "data:text/html,%3Ch1%3Erun-worker%20screenshot%3C%2Fh1%3E" "$screenshot"
  test -s "$screenshot"'

check "Playwright opens a headed 1366x1024 Chromium window under Xvfb" 124 /usr/bin/env sh -c '
  timeout 5 xvfb-run -a -s "-screen 0 1400x1100x24" \
    bunx --yes playwright@1.60.0 open \
      --viewport-size "1366,1024" \
      --user-data-dir /tmp/run-worker-playwright-headed-profile \
      "data:text/html,%3Ch1%3Eheaded%20run-worker%20browser%3C%2Fh1%3E"'

check "Bun can fork a Node child" 0 /usr/bin/env sh -c '
  probe_dir="$(mktemp -d)"
  trap "rm -rf \"$probe_dir\"" EXIT
  printf "process.exit(0)\\n" > "$probe_dir/child.cjs"
  bun -e "
    import { fork } from \"node:child_process\";
    const child = fork(process.argv[1], [], { execPath: \"node\", stdio: \"ignore\" });
    child.once(\"error\", () => process.exit(1));
    child.once(\"exit\", (code, signal) => process.exit(code === 0 && signal === null ? 0 : 1));
  " "$probe_dir/child.cjs"'

cred_dir="$(mktemp -d)"
trap 'rm -rf "$cred_dir"' EXIT
printf '%s' 'bot[bot]' >"$cred_dir/login"
printf '%s' 'smoke-token' >"$cred_dir/token"
chmod 0755 "$cred_dir"
chmod 0644 "$cred_dir/login" "$cred_dir/token"

docker run --rm --platform linux/amd64 \
  --entrypoint /bin/sh \
  --mount "type=bind,src=$cred_dir,dst=/var/run/secrets/software-factory/github,readonly" \
  "$img" -eu -c '
    command -v run-worker >/dev/null
    command -v git-credential-projected >/dev/null
    command -v gh >/dev/null
    test "$(git config --system credential.helper)" = /usr/local/bin/git-credential-projected
    credential="$(printf "protocol=https\nhost=github.com\n\n" | git-credential-projected get)"
    case "$credential" in
      *"username=bot[bot]"*"password=smoke-token"*) ;;
      *) exit 1 ;;
    esac
    gh --version >/dev/null
  '

exit "$fail"
