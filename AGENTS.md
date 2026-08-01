# AGENTS.md - software-factory

> **Scope: the active standalone repository, excluding `_archived/`.**
> Everything in this file and in `docs/` governs the Go module, console, and every
> image under `images/`. The Run Worker
> image is in scope because the credential boundary below spans Go code, container layout,
> and the pod specification; a
> standard that stopped at the Go code would disclaim one of its own enforcement sites.
> `_archived/` preserves the prior prototype byte-for-byte for later review and is
> excluded from active instructions, lint, build, generation, test, image, and CI
> surfaces.

`web/` is the console (ADR-0012): a TypeScript SPA, governed by
`docs/writing-scalable-typescript/`, not by `SoftwareStyle.md` or `.golangci.yml` below.

## What this is

A Go Temporal worker that autonomously works Tickets from its own Postgres (ADR-0012).
The stable `Dispatcher` admits dependency-ready `open` Tickets to one `WorkOnTicket`
workflow each. `WorkOnTicket` owns plan, implement, required CI, independent review,
bounded revision, and exact-reviewed-head squash merge. `MaintainFactory` periodically
repairs orphaned Run ownership and Run Worker generations. A confirmed merge records the
Run as `succeeded` and the Ticket as `done`; cancellation returns the Ticket to `open`;
terminal failure or exhausted budget records `failed` or `exhausted` and moves the Ticket
to `failed`.

Direct model calls run on the main worker. Each disposable per-Run pod has a credentialed
repository worker for fixed operations and a separate credential-free tool worker for
model-selected typed tools. The factory does not read GitHub Issues. Webhooks are
authenticated, deduplicated audit input only: they do not transition Tickets. GitHub
auto-merge is not used; the workflow performs the final exact-head merge itself. Progress
is the Run record in Postgres, read through the console.

What actually happens end to end - what each stage reads, may write and is trusted for, where
a human is required, and what is absent: [`docs/system-map.md`](./docs/system-map.md).
The activated runtime and its production proof are documented in
[`docs/system-map.md`](./docs/system-map.md) and
[`docs/e2e-proof.md`](./docs/e2e-proof.md).

## Layout

One Go module rooted here, not one per component, because more components are expected and
splitting a module later is cheap while unsplitting is not. Run
`scripts/regenerate.sh` to refresh committed API and database artefacts.

```
cmd/worker/        activated Temporal worker and readiness composition root
cmd/api/           authenticated Ticket and Run record API
cmd/blobs/         content-addressed transcript blob service
cmd/codec/         Temporal payload codec service
cmd/relay/         stateless GitHub webhook relay
cmd/run-worker/    fixed repository and GitHub activity worker
cmd/tool-worker/   credential-free typed-tool activity worker
internal/
  work/            domain vocabulary - every seam is expressed in these types
  config/          the only place os.Getenv is legal
  clock/           the only place time.Now is legal
  telemetry/       logging + Prometheus, injected
  workflows/       deterministic only - see the section below
  activities/      all side effects; declares the interfaces it consumes
  clients/         github, k8s, codex, codexauth - each seals its SDK
  store/           narrow Postgres interfaces over sqlc, sealed in store/storedb
  transcripts/     persisted conversation and transcript evidence
  prompts/         stage prompts + JSON schemas, go:embed
images/
  worker/          the worker image
  run-worker/      both per-Run worker binaries and repository toolchains
  relay/           the stateless GitHub webhook fan-out edge service
  api/             the factory API
  blobs/           the blob service
  codec/           the payload codec
web/                the console and same-origin API proxy image
```

Software Factory is one product with several processes in one Go module. Every release
builds the worker, Run Worker, relay, API, blob, codec, and console images together so a
shared module edit cannot ship beside a stale image.

## Where the standards came from

Adapted from the `software-factory` repo's SoftwareStyle, which was written for a Go
codebase maintained by agents. What we took, translated and deliberately skipped is
recorded in [`docs/style-adoption.md`](./docs/style-adoption.md) - read it before arguing
that a rule from that repo applies here, because several do not.

- Values and tenets: [`docs/SoftwareStyle.md`](./docs/SoftwareStyle.md)
- The wall: [`.golangci.yml`](./.golangci.yml)

## Priority ordering (resolves every trade-off, high beats low)

**Legibility > Correctness > Operability > Economy.** Machine performance is unranked -
this is LLM-latency-bound; below ~1s, don't care. Testability is a floor beneath all four
and is never traded.

## The floor

No unit test touches the real world. Every external edge - Responses, the k8s API, GitHub, the
clock, the filesystem - sits behind a narrow injectable interface so a test hands it a
fake. Temporal's `testsuite` covers workflow replay without a real server.

## The one thing this codebase gets wrong most easily

**Workflow code is not normal Go.** Inside `internal/workflows/**` you must use
`workflow.Now`, `workflow.Sleep`, `workflow.Go` and `workflow.SideEffect` - never
`time.Now`, `time.Sleep`, `go` or `rand`. Replay determinism depends on it, and a
violation surfaces later as a corrupted run, not a compile error. The linter enforces what
it can; the rest is on you.

`workflow.Context` is **not** `context.Context`. Activities and clients get the real one.

## Changing an existing workflow

**Treat a workflow command-sequence change as a history compatibility change.** Temporal
replays a workflow's complete history through the deployed code for every workflow task and
expects the commands it issues to line up with that history. If they diverge, Temporal refuses
to guess and reports a non-determinism error; the open execution can then wedge retrying
workflow tasks.

This applies to any edit that can change the ordered workflow commands: activity calls, child
workflow starts, timers, selectors that choose when to schedule a command, and their
command-producing branches. It does **not** by itself apply to an activity implementation body
or to a helper the workflow never calls. Normal `testsuite` tests prove intended new behavior;
they do not prove that a persisted old history still replays.

`Dispatcher` is the primary risk here. It is the singleton
`software-factory-target-dispatcher`, remains open for hours, and carries its latest
accepted policy over Continue-As-New, so a normal deploy commonly reaches an open run.
`WorkOnTicket` normally finishes sooner and is less exposed, but an open Ticket workflow
can still replay and is not exempt. `MaintainFactory` executions are finite, but their
command sequences have the same compatibility requirement while open.

When changing a command sequence, put the old and new decision branches behind
`workflow.GetVersion` **at the changed command branch**. Give the change a stable, unique ID
for that compatibility transition; do not add an unrelated marker elsewhere. Histories from
before the future change must keep replaying the prior target branch, while new histories take
the new branch. Keep the prior branch until no retained target history can need it. The v0
activation itself did not use this mechanism: operational quiescence closed every legacy
execution before target registration, and those legacy workflow sources and fixtures were
then removed. For a future target change, for example:

```go
version := workflow.GetVersion(ctx, "dispatcher-admission-v2", workflow.DefaultVersion, 1)
if version == workflow.DefaultVersion {
    // Preserve the activated v0 command sequence for existing target histories.
    return admitV1(ctx, ticket)
}
return admitV2(ctx, ticket)
```

When unsure whether an edit is compatible, replay an exported real or production-like history
against the changed workflow before deploying. Export the exact execution (include `--run-id`
when replaying a non-current run) from the same admin-tools context used by the runbook:

```sh
kubectl -n temporal run tmp-temporal-cli --rm -i --restart=Never \
  --image=temporalio/admin-tools:1.31.2 --command -- sh -c \
  'sleep 2; temporal --address temporal-server:7233 --namespace software-factory \
  workflow show --workflow-id <workflow-id> --run-id <run-id> --output json' \
  > <workflow-id>-<run-id>.json
```

Use `worker.WorkflowReplayer` in a focused Go test, registering the workflow exactly as the
worker does. `ReplayWorkflowHistoryFromJSONFile` consumes the JSON produced by `workflow show`:

```go
func TestReplayDispatcherHistory(t *testing.T) {
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(workflows.Dispatcher)

	require.NoError(t, replayer.ReplayWorkflowHistoryFromJSONFile(
		nil, "testdata/dispatcher-history.json"))
}
```

## Operating protocol

- TDD test-first for workflows and activities. The dispatcher's concurrency cap, pause and
  reconcile logic are unit tests, not things you find out about in production.
- Done = `golangci-lint` clean and relevant tests pass, verified by running them, not asserted.
- Never silence a linter. Fix the code.
- Stop and ask before anything irreversible or outward-facing.
