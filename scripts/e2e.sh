#!/usr/bin/env bash
set -euo pipefail

for command in docker jq go; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "e2e requires $command" >&2
    exit 1
  fi
done

container="software-factory-e2e-${PPID}-${RANDOM}"
artifact_dir="${PWD}/.artifacts/e2e"
result="${artifact_dir}/result.json"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

mkdir -p "$artifact_dir"
rm -f "$result"

docker run --rm --detach \
  --name "$container" \
  --publish 127.0.0.1:0:5432 \
  --env POSTGRES_USER=software_factory \
  --env POSTGRES_PASSWORD=software_factory \
  --env POSTGRES_DB=software_factory \
  postgres:17 >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$container" pg_isready -U software_factory -d software_factory >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
if ! docker exec "$container" pg_isready -U software_factory -d software_factory >/dev/null; then
  docker logs "$container" >&2
  echo "disposable PostgreSQL did not become ready" >&2
  exit 1
fi

published="$(docker port "$container" 5432/tcp)"
port="$(sed -E 's/.*:([0-9]+)$/\1/' <<<"$published")"
if [[ ! "$port" =~ ^[0-9]+$ ]]; then
  echo "could not resolve disposable PostgreSQL port from: $published" >&2
  exit 1
fi
export SOFTWARE_FACTORY_DATABASE_URL="postgresql://software_factory:software_factory@127.0.0.1:${port}/software_factory?sslmode=disable"
export SOFTWARE_FACTORY_E2E_RESULT="$result"

go test -count=1 -v -tags=e2e ./internal/e2e

jq --exit-status '
  .ticketState == "done" and
  .runOutcome == "succeeded" and
  .agentWorkflowStages == ["plan", "implement", "review"] and
  .merge == {"method":"squash","reviewedHeadMatched":true} and
  .activeRuns == 0 and
  .remainingRunWorkers == 0 and
  .modelAdapter == "fake-responses" and
  .githubAdapter == "fake"
' "$result" >/dev/null

echo "E2E acceptance artifact: $result"
