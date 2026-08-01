# v0 capability gates

This is the archived evidence record for PR 0.5. It describes observations of
the retired CLI prototype, not target or current runtime behavior. Production
uses `AgentWorkflow` and no longer installs or invokes the Codex CLI.

## Codex fresh-filesystem resume

The retired prototype sandbox image pinned `codex-cli 0.145.0`. On 2026-07-31, the exact
image (`sf-sandbox:local`, built from `images/sandbox/Dockerfile`) was run twice
with its normal authentication layout: an auth document mounted at
`/var/run/secrets/software-factory/codex-auth.json`, symlinked into a fresh
`CODEX_HOME` at `/work/.codex/auth.json`. The harness never emits the auth
document. Its negative disclosure check compares long auth string values
against every captured stdout/stderr file inside a mode-restricted temporary
directory, prints no matches or values, and deletes the directory on exit.

The first process emitted `thread.started` with thread ID
`019fba69-606e-73a0-97bb-10a563253354`, completed successfully, and ended with
a `turn.completed` envelope containing numeric `input_tokens`,
`cached_input_tokens`, `output_tokens`, and `reasoning_output_tokens`. Its
JSONL stdout crossed an HTTP boundary one event at a time into a separately
built and separately running append-only sink. The sink fsyncs every accepted
event before returning. It had received events while the target container was
still running, retained all four events after Docker removed the container and
its `/work` filesystem, and still exposed the terminal envelope and usage.
This proved the required durable sink shape. The activated runtime now persists
Agent Attempt, usage, checkpoint, and transcript evidence through its production
Store and blob-service boundaries.

A second, fresh target-image process was given that exact ID through
`codex exec resume <id> -`. It failed before producing a resumed turn:

```text
thread/resume failed: no rollout found for thread id 019fba69-606e-73a0-97bb-10a563253354
```

Conclusion: **fresh-filesystem resume is not proved and is currently
unavailable for a completed thread with the target Codex CLI/authentication
mode.** The controlled pre-identity probe starts the actual target `codex exec`
process with an open FIFO withholding prompt bytes and EOF. The container
wrapper records the live child PID and `/proc/<pid>/exe`; Docker process
inspection independently verifies the `codex exec` command before the harness
kills it with exit 137. On this Apple Silicon host, the amd64 executable appears
through Docker Desktop's Rosetta path. The probe found zero rollout files and
no `thread.started` event. A second probe captured thread ID
`019fba69-8afe-7e31-bbac-9c8ae48d3075`, killed that container with exit 137
immediately after `thread.started`, and received the same `no rollout found`
error from a fresh container. The actual pre-identity process, absence of
resumable state, incremental sink delivery, post-deletion sink survival,
terminal envelope, required usage fields, both kill points, and negative
disclosure scan were all verified; the harness fails if any required probe does
not occur.

The safe v0 boundary is therefore explicit: the retired provider-thread model
had no cross-filesystem resume. Do not persist Codex rollout directories, and
do not claim provider resume from this evidence.

The target runtime does not use this continuation mechanism. It clones the
last successful implement `AgentWorkflow` conversation into a new
attempt-owned identity and appends fresh structured feedback. The main worker
persists terminal result, usage, and bounded transcript evidence before the
containing Step completes. A Run Worker loss is classified by the child
workflow, then recovered or retried before the Run's immutable deadline; it never
reuses a provider thread or a partial failed conversation.

The one-off resume harness was deleted with the CLI runtime. The observations
above remain historical evidence; they are not a production verification step.

## Temporal Session harness

`internal/runworkercapability/session_integration_test.go` uses the Go SDK's
real Temporal CLI dev server (`testsuite.StartDevServer`), not the unit
`TestWorkflowEnvironment`. It pins the dev-server download to CLI `v1.8.1`.
The harness starts one main worker and two separately registered private
workers as distinct helper subprocesses. Each helper receives only a marker
name and resolves it under its own configured temporary root. The first worker
writes and reads `repository-state-v1` across two Session activities; both
results report its identity and OS process ID. A direct activity on the second
worker reports its different identity/process and that the same marker name is
absent from its root. A main-control activity still runs on the main worker. A
second scenario stops and replaces the main worker between the two Session
activities; both retain the original private-worker process, root marker, and
identity, while the main-control activity runs on the replacement main worker.

Run it with:

```sh
cd <software-factory-clone>
go test -race -tags=integration ./internal/runworkercapability
```

This proves affinity and main-worker restart behavior only. It intentionally
also stops the active private-worker subprocess, verifies that the Session
reports failure while main-control work remains callable, then creates a
replacement Session on a new helper process and second root. Its first activity
proves the original marker is absent from that root; its next activity writes
and reads `replacement-state-v1` while reporting the replacement identity and
process ID.
It proves Temporal's filesystem/routing capabilities and failure boundary only.
Domain tests for the activated `WorkOnTicket` runtime separately prove Agent
Attempt closure, checkpoint restoration, generation replacement, and preservation
before the original Run deadline. Resumed Codex execution is intentionally absent.

## Target dispatcher replay fixture

`internal/workflows/testdata/target-dispatcher-admission.json` is an export
from a Temporal CLI dev-server execution of the pre-activation v0 `Dispatcher`. It
retries one no-work `AwaitDispatchableTickets` attempt, then completes the
next wait and starts one `WorkOnTicket` child, preserving the
wait-to-admission command sequence before the registration cutover made the
workflow live.
`TestTargetDispatcherHistoryReplays` registers the activated Dispatcher exactly
as the worker does and replays that checked-in JSON export.

Regenerate it only through the manual dev-server test when intentionally
refreshing this compatibility evidence:

```sh
cd <software-factory-clone>
go test -tags=manual -run '^TestExportTargetDispatcherHistory$' \
  ./internal/runworkercapability
```

## Activated vocabulary

Migration 11 removed the legacy lifecycle vocabulary and renamed the opaque
execution field to `agent_execution_id`. Ticket state is exactly `open`, `active`,
`done`, or `failed`. Run outcome is exactly `succeeded`, `canceled`, or `failed`.
There are no deployed `working` or `review` Ticket states, provider
thread fields, legacy Run projections, or webhook-owned lifecycle transitions.
