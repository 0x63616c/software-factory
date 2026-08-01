# SoftwareStyle — software-factory

> **Scope: the active standalone repository, excluding `_archived/`.** Adapted from
> the prior prototype's style guide, which took the *idea* of a house style from
> TigerBeetle's TigerStyle. The archived tree retains its historical standards.
> What was adopted, translated and dropped is recorded in
> [`style-adoption.md`](./style-adoption.md).

The north star for Go in this directory. Rule vs this doc → this doc wins. Rule vs rule →
the priority ordering decides. Correctness comes from *types, linters and tests*, not
runtime assertions.

---

## The priority ordering

Resolve every trade-off in this order. Higher beats lower.

> **Legibility > Correctness > Operability > Economy**

Machine performance is **not on the list**. This system waits on an LLM and on a
Kubernetes API; below ~1s, don't care.

### Legibility — *understandable in isolation?*
Understand a piece **without loading the whole system**.
- **Test:** open one file cold — can you tell what it does, assumes, and breaks on?
- **In code:** deep modules, narrow interfaces, intent-revealing names, invariants in
  types, no hidden global state.

### Correctness — *right thing, loud on failure?*
Does what it's specified to; when it can't, fails **loud and early**, never limps.
- **Test:** on a malformed Ticket or a wedged credential, does it halt with a clear error,
  or open a confidently wrong PR?
- **In code:** parse-don't-validate at boundaries, illegal states unrepresentable, no
  empty catch, no default-to-zero hiding a bug.

### Operability — *see it and stop it while it runs?*
It runs unattended. You must be able to **see** and **stop** it.
- **Test:** mid-run at 3am — can you tell which ticket is on which stage, why it decided
  what it did, and stop it without leaving a half-pushed branch?
- **In code:** structured logs at decisions, Temporal history as the audit trail, the
  console over the run record in Postgres, idempotent stages, clean cancellation.

### Economy — *LLM spend and human wait.*
Real here in a way it usually isn't: every stage costs subscription quota shared with the
human's own sessions. **Architectural, not micro-optimization** — cut it with fewer stages,
cheaper models, richer structured handoff so a stage doesn't re-explore the repo from cold.
The tiebreaker among equal designs: pay tokens for a correctness or operability win, never
for a redundant pass that buys nothing.

### The trades
- **Legibility > Correctness.** An agent maintains this code. What it can't understand it
  can't safely change, so illegible-but-correct rots to incorrect on the next edit.
- **Correctness > Operability.** Broken invariant mid-run: halt that ticket. "Log and keep
  going" produces a plausible PR that wastes a review.
- **Operability > Economy.** Decision logs, transcripts and token accounting cost
  something. Pay them — you cannot run an unattended code-shipping system you can't watch.

---

## Testability is a floor, not a dial

Never traded. Untestable code is a blind agent guessing.

**The rule:** *no unit test touches the real world.* Every external edge — Responses, the
Kubernetes API, GitHub, the clock, the filesystem — sits behind a narrow injectable
interface, so a test hands it a fake. Deterministic: injected clock, no real time,
randomness, network or model.

Temporal's `testsuite` is **not** the real world — it runs workflows against a virtual
clock with mocked activities, hermetically. Prefer it to hand-rolled orchestration fakes.
This is *why* construction is manual: a test hands a fake to a constructor with zero magic.

Database-backed integration tests run in CI and explicitly skip locally when `SOFTWARE_FACTORY_DATABASE_URL` is absent.

---

## The tenets

1. **Make correctness mechanical.** If a linter or the type system can enforce an
   invariant, it **must**. Agents forget; walls don't.

2. **No escape hatches.** Never silence a tool — fix what it flags. Banned: `//nolint`,
   ignored errors, bare `any`, silent `recover`.

3. **Deep modules, narrow door.** Narrow stable public surface, deep private internals.
   Growth goes *down* into private helpers, never *out* into the surface. If a change
   forces you to export more, the seam is wrong.

4. **No leaky abstractions.** A third-party type must not cross a module boundary.
   `client-go` stays in `clients/k8s`; `go-github` stays in `clients/github`; raw codex
   JSONL stays in `clients/codex`. A leak infects the codebase with a dependency's
   worldview and kills testing.

5. **Prefer the elegant dependency, within the bar.** Not minimalists. Bar: readable
   surface, widely used, maintained, permissive licence, small transitive footprint. Pin
   everything. Wrap only risky or leaky ones.

6. **Micro-libraries only when a pattern earns it.** Extract *after* the third repetition,
   not before.

7. **Fail fast, fail *helpful*.** Config failure at startup → clear message and non-zero
   exit, never a panic dump. A missing credential is a user error, not a programmer bug.

8. **Failures never escape their ticket.** A bad Ticket fails its own FactoryWorkTicket
   workflow, visibly, and never takes down the worker or another ticket. Temporal converts an
   activity panic into a failure for free — don't defeat it with a bare `recover`.

9. **Observability is a platform feature, not a per-call chore.** Logging is baked into the
   primitives — clients and activities log themselves. Leaf code rarely logs by hand;
   nobody can forget. Structured, verbose by default, correlated by Temporal workflow and
   run ID, **to stdout as JSON** so the cluster's Loki pipeline picks it up. Written for a
   future agent debugging this at 3am.

10. **Workflows are deterministic; activities do the work.** Every effect — network, disk,
    clock, randomness, subprocess — lives in an activity or a client. Workflow code
    orchestrates and nothing else. This is not a preference: replay correctness depends on
    it, and a violation shows up later as a corrupted run rather than a failed build.

11. **Don't rely on reading.** Nothing critical may depend on an agent *choosing* to read a
    doc. Climb the pyramid: *mechanical (lint, types) > always-loaded context (`AGENTS.md`)
    > hooks > docs.*

12. **Single source of truth — code as much as docs.** Every fact has one authoritative
    home; everything else *refers* to it. Config only in `config`, wall-clock time only
    behind `clock`, each enum defined once, one composition root. Docs held to the same
    rule: never restate what code, types or a linter already express. Cross-reference by
    name, not number. Copies drift; the only thing that stays true has nothing to desync
    with.

---

## Mechanical enforcement

Tenets that can be walls are walls: [`.golangci.yml`](../.golangci.yml).

| Concern | Enforced by |
|---|---|
| Workflow determinism (`time.Now`, `rand`, real I/O banned in `workflows/`) | `forbidigo` + `depguard` |
| SDK types sealed in their client package | `depguard` |
| `os.Getenv` outside `config`; `time.Now` outside `clock` | `forbidigo` |
| Ignored errors | `errcheck` |
| Exhaustive enum switches (stage, breaker state) | `exhaustive` |
| `context` not struct-stored, propagated correctly — *activities and clients only* | `containedctx`, `contextcheck`, `fatcontext` |
| Doc comments on exports and packages; comments end in a period | `revive`, `godot` |
| Data races | `go test -race` |

Judgment-tier standards a linter can't catch — narrow-door, no-leaky, parse-don't-validate,
comment and test-name quality, and **naked `go` inside workflow code** — are review-tier.

Want a rule off? Fix the code, not the config (tenet 2).

---

## Construction

- **Parse, don't validate.** Turn external input — Ticket bodies, codex JSONL events,
  k8s pod status — into a **typed domain value at the boundary**. Inside, data is valid *by
  type* and never re-checked.
- **Required deps positional; optional config via functional options.** `New` sets
  defaults, applies options, then validates once. The `options` struct is unexported.
- **No usable-but-invalid zero value.**

## Errors

- **Never `return err` bare.** Wrap on the way up with context — which ticket, which stage,
  which command.
- **Error kinds map onto Temporal's taxonomy, not a parallel one.** A malformed Ticket or an
  unusable plan is a **non-retryable** `ApplicationError`; a transient GitHub or Kubernetes
  failure is retryable. One taxonomy.
- Invariant violations are assertion failures, not control flow.

## Concurrency and context

The rule that matters — context is the first parameter, threaded down, never struct-stored,
cancellation always honoured — holds everywhere. **The type differs by package:**

- `internal/activities/**`, `internal/clients/**`, `cmd/**` — real `context.Context`.
  Standard rules, `go test -race`, no naked goroutines.
- `internal/workflows/**` — `workflow.Context`. Use `workflow.Go`, `workflow.Sleep`,
  `workflow.Now`, `workflow.SideEffect`. Temporal's runtime supervises and replays these;
  a hand-rolled worker abstraction here would not be replay-safe.

Do not try to force one uniform context discipline across both. That is the single place a
Go style guide written without Temporal in mind will mislead you.

## Interfaces and packages

- **Interfaces are consumer-side and small** — declared where used, only the methods that
  consumer needs. *Accept interfaces, return concrete types.*
- **No grab-bag packages.** `util`, `common`, `helpers`, `misc`, `shared` — banned. A thing
  belongs to the domain it serves.

## External process execution

The active runtime calls the Responses interface directly and never invokes the
retired Codex CLI. Every repository-tool process goes through a fixed-operation
Run Worker boundary. The rules are non-negotiable because Ticket titles and
bodies are **attacker-controllable text**:

- **Argv-only.** Arguments are an explicit `[]string`. Never interpolated into
  `sh -c "<string>"`. No shell, no injection surface, no quoting bugs.
- **End-to-end.** The guarantee only holds if the Run Worker's own entrypoint doesn't
  reintroduce a shell. Audit it.
- **Context-aware**, so an activity timeout actually kills the exec stream rather than
  orphaning it.
- **Output and exit code captured**, never swallowed. stderr is evidence.
- **Behind an interface**, so unit tests inject a fake and never reach a cluster.

## Identity

Do not mint IDs beyond the one generator that earns it. Temporal workflow and run IDs
already exist and are already authoritative, and a Ticket's small database id is minted
by Postgres. Workflow IDs are self-describing by construction, derived from the Ticket id
(`factory-ticket-<id>`). That namespace is deliberately disjoint from the retired
`work-ticket-<n>` scheme, so a Ticket can never share a Temporal history lineage with the
GitHub issue of the same number. Add another generator seam only if something needs an
identity neither Postgres nor Temporal already gives it.

## Time and units

- **UTC only.** `time.Now` and `time.Local` are banned outside `internal/clock`; workflow
  code uses `workflow.Now`.
- **Read time through the injected clock** — polling, backoff and token refresh are all
  clock-driven and all must be testable.
- **No sleeps in tests.** Advance a fake clock, or Temporal's virtual one.
- **RFC3339 UTC on the wire.**
- **Units at the end of names** for raw numbers (`sizeBytes`, `maxAgeSeconds`). A
  `time.Duration` already carries its unit — don't suffix it.

## Code prose

- **Every exported symbol and package has a doc comment** starting with its name, ending in
  a period.
- **Comment the *why*, not the *what*.**
- **Short but sweet.** Cut filler. Half the length, same meaning → do it. Brevity is
  legibility and costs fewer tokens on every load.
- **No commented-out code. Scoped `TODO(scope):` only.**
- **Test names read as behaviour sentences.** Present tense, one behaviour each, no
  "should", "test" or "correctly": `holds the concurrency cap when a child fails`.

## Prompts are product

`internal/prompts/` holds the stage prompts and JSON schemas, embedded with `go:embed`.
They are the highest-churn, highest-leverage part of this service — treat a prompt change
with the same seriousness as a code change, and keep them as files so they read and diff
as prose.
