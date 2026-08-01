# What we took from `software-factory`, and what we didn't

> **Historical context for the active standalone implementation.** The original
> prototype and its complete standards remain under `_archived/`.

The original standalone prototype spent its foundation phase writing a Go style guide
and 26 ADRs for a codebase maintained by agents. The activated implementation adopted a
subset while it lived in `world-wide-webb`; this document records that historical
translation. The complete original files are now preserved under `_archived/`.

Its own framing splits *Layer A* (the factory's own Go, governed by its SoftwareStyle) from
*Layer B* (standards it hands to projects it operates on). We are Layer-B-shaped: a
consumer picking what generalises, not a sibling inheriting a system.

## Adopted essentially unmodified

| Source | Why it transfers |
|---|---|
| **Priority ordering** — Legibility > Correctness > Operability > Economy, performance unranked | Exactly right for an LLM-latency-bound system. Economy is *more* load-bearing here since stages spend shared subscription quota. |
| **Testability floor** — no unit test touches the real world | Our edges are enumerable: codex, k8s, GitHub, clock, filesystem. |
| **Shell-out discipline** (their ADR-0021) | The most valuable import. See below — it carries a security rule specific to us. |
| **Testing tiers** (their ADR-0012) | Reads as a spec for this service with "LLM" swapped for "codex". |
| **Construction** — manual injection, one composition root, positional deps, functional options | Correct at this size; no DI framework. |
| **Boundaries** — parse-don't-validate, consumer-side interfaces, no grab-bag packages | Directly relevant to issue bodies, JSONL events and pod specs. |
| **Code prose** — doc comments, why-not-what, scoped TODOs, behaviour-sentence test names | Cheap, compounding. |
| **Time and units** — UTC only, injected clock, no test sleeps | We have a poll loop, backoff and token refresh. All clock-driven, all must be testable. |

## Adopted with translation

**Logging.** Their tenet says logs go to a *file*, never the terminal — because a TUI owns
their terminal. We are a pod. Structured JSON to **stdout** is correct here; that's what the
cluster's Loki pipeline consumes. We kept the discipline (injected, never global, baked into
primitives, correlation IDs) and dropped the mechanism.

**Context and concurrency** (their ADR-0019). Their rule is `context.Context` everywhere, no
naked goroutines. Workflow code runs under `workflow.Context` — a *different type* — and must
use `workflow.Go` / `workflow.Now` / `workflow.Sleep` / `workflow.SideEffect` or replay
breaks. We split the rule by directory: standard `context.Context` discipline in
`activities/` and `clients/`, Temporal's contract in `workflows/`, and lint config scoped to
match. Applied verbatim, this ADR would have been actively wrong.

**Errors** (their ADR-0006). Wrap-always and the three-kind taxonomy transfer. But Temporal
already has a retryable/non-retryable `ApplicationError` taxonomy, so ours **maps onto
that** rather than inventing a parallel one.

**Config** (their ADR-0007). The shape — one typed struct, one `Validate()`, `os.Getenv`
sealed in `config` — transfers. Their library choice does not; at this size, stdlib parsing
of env plus a mounted ConfigMap is enough.

**Identifiers** (their ADR-0017). Self-describing IDs are a good idea, but GitHub issue
numbers and Temporal workflow/run IDs already exist and are already authoritative. We make
workflow IDs self-describing (`work-ticket-<n>`) and skip minting anything.

**Secret redaction** (their ADR-0020). We hold a GitHub App private key and a codex OAuth
refresh token, and the repo has a standing never-print-secrets rule — but a general
self-redacting `Secret` type is ceremony at this size. A one-file masked `String()` /
`LogValue()` on those specific fields is proportionate.

**Lint wall.** Copied and repointed: dropped their TUI and SQL `depguard` rules and
`ginkgolinter`, added workflow-determinism rules and SDK-sealing rules of our own. Their
"one check list, hooks and CI run it unchanged" pattern is honoured by extending this
repo's existing `lefthook.yml`, not importing a second one.

## Deliberately not adopted

- **The TUI / EventSink / headless-engine apparatus.** No terminal UI exists here.
- **The supervised-worker primitive.** Temporal already provides named units of work,
  panic-to-failure per activity, cancellation propagation, retries and full history.
  Building a parallel layer would duplicate the SDK, and inside workflow code it would not
  be replay-safe.
- **The sqlite persistence stack.** The factory uses Postgres with pgx, goose and sqlc;
  state in Temporal history and a Kubernetes Secret remains deliberately separate.
- **Layer A / Layer B, standards-as-data, the skills bundle and symlink machinery.** That
  is infrastructure for a program that operates on arbitrary target repos. We are one
  worker, and world-wide-webb already has its own skills convention.
- **Lifecycle state machines as a generic primitive.** Stage sequencing is Temporal control
  flow. The dispatcher's in-flight status is the one place a typed enum with a transition
  guard might earn itself later.
- **Ginkgo/Gomega.** Kept the behaviour-sentence naming discipline, dropped the framework.
- **`internal/id` as built, and any DI-framework escalation path.** Not needed at this size.

## If this ever goes repo-wide

It hasn't, and nothing here assumes it will. Expanding scope would be its own decision with
its own ADR — most of the above is Go-specific and would need a second translation pass to
mean anything for the TypeScript side.
