# The software factory, end to end

This is the operational map of the activated v0 runtime. It names the source
symbols that own each boundary so changes can be checked against code instead
of preserving this prose by inertia.

## One glance

```text
an open, dependency-ready Ticket in the factory Postgres
        |
        | Dispatcher, workflow ID software-factory-target-dispatcher
        | acknowledged unpaused policy, maximum one Run in flight by default
        v
factory-ticket-<ticket-id> (WorkOnTicket)
        |
        | atomically claim Ticket: open -> active, active_run_id = Run ID
        | create one generation-scoped Run Worker pod
        | clone repository and create a Run-owned branch
        | AgentWorkflow(plan)
        | AgentWorkflow(implement) -> synchronize draft PR -> required CI
        | AgentWorkflow(review)
        |   blocking findings or merge conflict -> another implement cycle
        | clean review -> mark PR ready -> exact reviewed-head squash merge
        v
confirmed merge: Run succeeded, Ticket done
terminal failure/deadline: Run failed, Ticket failed
cancellation or abandoned ownership repair: Run canceled, Ticket open
```

There is no GitHub Issue work queue, webhook-owned Ticket transition, or
GitHub auto-merge step. The factory owns Tickets in Postgres. `WorkOnTicket`
waits for the required checks, performs its own review, and requests a squash
merge bound to the exact reviewed head. Only the Confirmed Merge transaction
may move the Ticket to `done`.

## Control plane

### Dispatcher

`workflows.Dispatcher` is the stable singleton
`software-factory-target-dispatcher`. It polls the dedicated
`software-factory-dispatcher-control` task queue for acknowledged policy
updates. Ticket admission uses `activities.AwaitDispatchableTickets` on the
main `software-factory` task queue. No-work cadence is Temporal activity retry
state, not a workflow timer.

Worker activation in `cmd/worker/activation.go` is deliberately ordered:

1. Refuse activation while a legacy workflow or legacy Ticket state remains.
2. Start the control worker.
3. publish the complete resolved Dispatcher policy through Update-With-Start
   and wait for `APPLIED` or `ALREADY_CURRENT`.
4. Reconcile the `software-factory-maintain` Schedule.
5. Start the main worker.
6. Mark `/readyz` ready.

The default policy comes from `work.DefaultDispatcherPolicy`: it is unpaused,
admits one Run at a time, and carries the immutable target Run policy. A policy
is part of the child input at admission, so a later publication cannot change
an already-running Run.

`Dispatcher` drains tracked children before Continue-As-New. The continued
input carries the last accepted policy. The stable workflow ID and separate
control queue let a new worker publish policy before it starts polling the main
queue.

### MaintainFactory

`workflows.MaintainFactory` is a finite recovery pass started every five
minutes by the `software-factory-maintain` Temporal Schedule with overlap
policy `SKIP`. Worker boot creates or updates the Schedule definition while
preserving its live paused state.

Each pass compares three facts:

- active Ticket/Run ownership in Postgres;
- open `factory-ticket-<ticket-id>` executions in Temporal;
- generation-labelled Run Worker pods in Kubernetes.

It leaves a matching open owner alone. It deletes Run Worker generations for
terminal or abandoned Runs, records an abandoned Run as canceled, and returns
only that Run's still-owned Ticket to `open`. This is repair, not dispatch.

## Durable state and identity

Ticket state is exactly:

| state | meaning |
|---|---|
| `open` | filed and eligible when every blocker is `done` |
| `active` | owned by the one Run named by `active_run_id` |
| `done` | terminal Confirmed Merge; satisfies dependencies |
| `failed` | terminal failure or semantic deadline; a human may move it to `open` |

Run outcome is exactly `succeeded`, `canceled`, or `failed`.
`succeeded` requires immutable `reviewed_head` and `merge_sha` evidence.
Cancellation returns its Ticket to `open`; failure moves it to `failed`. A
`done` Ticket never reopens.

Historical Runs remain readable without rewriting their evidence. The API may
therefore return the retired `exhausted` outcome and `agent_attempt_budget` or
`review_budget` failure kinds for rows created before cumulative limits were
removed. New workflow executions cannot write those values.

| identity | form |
|---|---|
| Dispatcher workflow | `software-factory-target-dispatcher` |
| Ticket workflow | `factory-ticket-<ticket-id>` |
| Run-owned branch | `software-factory/factory-ticket-<ticket-id>/<run-id>` |
| Agent Attempt workflow | `agent/<run-id>/step/<ordinal>/attempt/<attempt-no>` |
| Run Worker generation | `run-worker-<run-id>-g<generation>` |

The Store atomically claims an `open` Ticket by setting `active_run_id`. Every
Step, Attempt, checkpoint, terminal transition, and recovery write is fenced by
that ownership. Temporal workflow IDs provide orchestration identity; they do
not replace the database ownership check.

Steps are ordinal and durable. Infrastructure, repository, CI, agent, review,
and merge operations record their start and terminal result. Agent Attempts
record model, effort, execution identity, usage state, token counts, result,
and transcript reference. Raw transcript and conversation bodies are stored by
reference in the blob service rather than copied into Temporal history.

## WorkOnTicket

`workflows.WorkOnTicket` owns the complete Run lifecycle:

1. Validate the admitted policy and atomically claim the Ticket.
2. Read any canceled predecessor checkpoint for safe branch recovery.
3. Provision Run Worker generation one and acquire a Temporal Session.
4. Clone the repository onto the generation's shared `/work` volume.
5. Run one plan and one initial implement Agent Step.
6. Create or update the Run-owned draft pull request.
7. Wait for every required check in the immutable policy, currently
   `test-software-factory`, on the exact candidate head.
8. Run an independent review Agent Step.
9. Feed red CI, blocking findings, head changes, base refreshes, and merge
   conflicts into bounded new implement Attempts.
10. Mark a clean candidate ready and request an exact-head squash merge.
11. Atomically record Confirmed Merge, finish the Run, move the Ticket to
    `done`, and delete the Run Worker.

The semantic deadline reserves time before the hard execution deadline for
finalization and cleanup. Agent Attempts and review Steps have independent
deadlines. Terminal errors are classified into stable failure kinds; raw errors
do not drive later business decisions.

A lost Run Worker generation fails its Temporal Session. `WorkOnTicket`
deletes that generation, provisions the next generation, restores the latest
durable repository checkpoint, and continues within the original Run deadline.
It never resumes a partial provider conversation. A new Agent Attempt clones
only a successful prior conversation reference and appends structured
feedback.

## Run Worker isolation

One digest-pinned Run Worker pod exists per active Run generation. It has two
containers built from the same image and a shared `emptyDir` checkout:

| container | authority |
|---|---|
| `run-worker` | fixed typed repository, GitHub, CI, and checkpoint activities; projected GitHub and capability Secrets |
| `tool-worker` | model-selected typed tools inside the checkout; no projected Secret, provider credential, or Kubernetes token |

The containers poll distinct generation-specific Temporal queues. The
credentialed `run-worker` cannot execute model-selected argv. The
credential-free `tool-worker` cannot read the repository/GitHub capability
mounts because they are mounted only into its sibling. Both run as uid 1000
without a service-account token, privilege escalation, or added Linux
capabilities. The main worker creates and deletes pods through the Kubernetes
API; it never uses `pods/exec` or remote file transfer.

Direct Responses model calls, prompt rendering, lifecycle evidence, and final
transcript persistence stay on the main worker. Only the typed tool activity is
routed to `tool-worker` through the Agent Attempt's Session.

## Pull requests, CI, and webhooks

Repository operations are workflow-owned and checkpointed. The model may edit,
test, and commit inside the checkout, but fixed repository activities own
clone, push, PR synchronization, readiness, and merge.

`WorkOnTicket` accepts a merge only when GitHub confirms that the merged head
is the exact head that passed required CI and the latest review. A changed head
returns to CI and review. Text conflicts or a required base refresh return to
implement. Ruleset rejection and exhausted GitHub availability are terminal,
classified Run failures.

The public relay still forwards authenticated GitHub deliveries to
`/v1/hooks/github`. `internal/webhook.TargetHandler` authenticates and
deduplicates them for audit only. It does not change Ticket or Run state.
There is no auto-merge enablement anywhere in the target pipeline.

## API, console, and records

[factory.worldwidewebb.co](https://factory.worldwidewebb.co) is behind
Cloudflare Access. Nginx serves the console and proxies `/api/*` to the API.
The API applies embedded Postgres migrations before it binds `/healthz`, which
is why API readiness is also the first migration-success signal.

The console reads the factory's Postgres record through the API. It shows
Tickets and their dependency graph, Runs, ordinal Steps, semantic Agent
Attempts, usage, failure classifications, Confirmed Merge evidence, and
downloadable transcripts. It does not read GitHub Issues or a dispatcher-state
projection.

| record | authority |
|---|---|
| workflow decisions | Temporal history in namespace `software-factory` |
| Ticket, Run, Step, Attempt, checkpoint, and merge evidence | software-factory Postgres |
| transcript and conversation bodies | content-addressed blob service |
| service logs | Loki |
| metrics | Prometheus |
| user-facing operational view | console and authenticated API |

## Human boundaries

A human files and prioritizes Tickets, resolves failed work, and
may return a failed Ticket to `open`. GitHub's ruleset still requires the
configured Code Owner approval for ordinary actors. The Software Factory App
may bypass that approval requirement only for pull-request merge, while the
separate `test-software-factory` required-check ruleset has no bypass actors.

The workflow, not a human and not GitHub auto-merge, performs the final
exact-head squash merge after its own review and required-check proof. Merging
to `main` triggers the normal CI and production deployment.

## Historical boundary

Legacy workflows, sandbox pods, `working`/`review` Ticket states, webhook
transitions, and auto-merge belong to the completed v0 cutover only. The cutover
runbook retains their exact resource names because it is the audit record for
removing them. They are not valid templates for new runtime code.
