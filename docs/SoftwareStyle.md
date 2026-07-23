# SoftwareStyle

SoftwareStyle is this project's own opinionated style guide — the idea of a project
having one is borrowed from TigerBeetle's TigerStyle, but the content here is ours,
and deliberately different. **SoftwareStyle optimizes a software factory written and
maintained by agents, for agents.** Its correctness comes from *types, linters, and
tests* enforced at compile-time and CI — not from runtime assertions.

This document is the north star. When a specific rule and this document conflict,
this document wins. When two rules conflict, **the priority ordering below decides.**

Architectural decisions (the *why* behind a specific structural choice) live as
ADRs in [`docs/adr/`](./adr/). This document holds the *values and tenets*; the ADRs
hold the *decisions that fall out of them*. Read both.

---

## The one idea everything hangs off

This repo is a **software factory**: a TUI that takes tickets and produces code
merged to production. But it is built to operate on *any* codebase — Go, Python,
TypeScript, whatever — and one of those codebases is **itself**.

That forces a split that runs through the whole design:

- **Layer A — the factory's own code.** How we write the Go that *is* the factory.
  This document.
- **Layer B — the standards the factory applies to whatever it's building.** Owned
  by each *target* project, not by us. Unknown, per-target, pluggable data.

These are **the same mechanism, different content.** The factory consumes a target's
standards as *data* (an `AGENTS.md` + `skills/` + lint config it reads from the repo).
Our own standards live in exactly those files. So **this repo is the factory's first
target project** — we dogfood the format from day one, and self-hosting is nearly free.

The load-bearing consequence: **the factory engine is language-agnostic. All
language- and standards-specific knowledge lives in data (the standards bundle),
never baked into the engine's code.**

---

## The priority ordering

When a trade-off has to be made, resolve it in this order. Higher beats lower.

> **Legibility > Correctness > Operability > Economy**

Machine performance (cache-friendliness, allocation, sub-microsecond anything) is
**not on this list.** This is a human-scale, LLM-latency-bound system. Below ~1s,
we do not care. "Don't be stupid" is the only rule; it is not a value we rank.

### Legibility — *can it be understood and verified in isolation?*
A person or agent can understand a piece **without loading the whole system**, and
can *verify* they understood it right.
- **Test:** open one file cold. Can you tell what it does, what it assumes, and what
  breaks it — without opening five others?
- **In code:** deep modules with narrow interfaces, names that say intent, invariants
  encoded in types, no hidden global state.

### Correctness — *does it do the right thing, and fail loud when it can't?*
The program does what it's specified to do, and when it doesn't, it fails **loud and
early** instead of limping.
- **Test:** given bad input or a broken invariant, does it halt with a clear error,
  or silently produce a wrong result?
- **In code:** validate at boundaries (parse, don't validate), make illegal states
  unrepresentable, no empty catch, no default-to-zero fallback that hides a bug.
- **Scope note:** "correctness" here means *the factory program behaves* — ticket
  state isn't corrupted, the pipeline moves as designed. It says **nothing** about the
  quality of the code the factory emits; that is Layer B, a runtime concern governed
  by the target's own standards.

### Operability — *can you see it and stop it while it runs?*
While running autonomously, you can **see** what it's doing and **stop/reverse** it.
- **Test:** it's mid-run on a ticket at 3am. Can you tell which stage it's in, why it
  chose what it chose, and kill it without corrupting state?
- **In code:** structured logs at decisions, observable state, idempotent/reversible
  steps, clean cancellation.

### Economy — *LLM spend and human wait time.*
Wall-clock the human feels, and the LLM bill. **This is architectural, not
micro-optimization** — you cut it with fewer/cheaper calls, caching, batching, and
model choice, not tight loops.
- Economy is the **tiebreaker** among otherwise-equal designs. You *will* pay tokens
  for a correctness or operability win (e.g. a verification pass). You will *not* pay
  tokens for a third redundant pass that buys nothing.

### The trades, made explicit
- **Legibility beats Correctness.** Splitting a clever function into three obvious
  ones, or adding a typed wrapper that *can't* be misused — take the legible version. An
  agent maintains this code; what it can't understand, it can't correctly change, so
  illegible-but-correct rots into incorrect on the next edit.
- **Correctness beats Operability.** A broken invariant mid-run: halt it. Don't
  "log and keep going" — a factory running on corrupted state produces confident
  garbage.
- **Operability beats Economy.** Structured logs at every decision and idempotent
  steps cost cycles and tokens. Pay them. You cannot run an autonomous
  code-shipping system you can't watch and stop.

---

## Testability is a floor, not a dial

Testability is **not** ranked among the four values — it is a **non-negotiable
property the whole codebase must have**, underneath the ranking. You never trade it
away. It is the substrate of the agentic loop (*change → run test → read result →
decide*): untestable code is a blind agent guessing.

**The rule:** *No unit test may touch the real world.* Every external edge — the LLM,
shell-outs, the clock, the filesystem, the network, the terminal — sits behind a
narrow injectable interface, so a test hands it a fake. Tests are deterministic:
injected clock, no real time/randomness/network/model calls.

In-memory sqlite (`:memory:`) is **not** "the real world" in the forbidden sense — it
is hermetic, deterministic, and spun fresh per test. Prefer it to mocking the store.

This is *why* we use manual constructor injection (see ADR-0004 / the DI decision):
it lets a test hand a fake to a constructor with zero magic.

---

## The tenets

Each tenet states the rule, the *why*, and — where possible — how it is enforced
**mechanically**. See *Mechanical enforcement*.

1. **Make correctness mechanical.** If an invariant can be enforced by a linter,
   codegen, or the type system, it **must** be — never left to discipline. Generated
   code is welcome when it removes a sharp edge. *Why:* agents (and humans) forget;
   walls don't. This is the strongest lever we have.

2. **No escape hatches.** You fix the problem; you never silence the tool that found
   it. No `//nolint`, no ignored errors (`_ = f()`), no bare `any`/`interface{}`
   where a real type belongs, no silent `recover`. *Why:* an escape hatch converts a
   found defect into a hidden one.

3. **Deep modules, narrow door.** A module is a **small door, a big room** — a narrow,
   stable public surface hiding arbitrarily deep internals. Growth goes *downward* into
   private sub-packages, never *outward* into the public surface. If a change forces
   you to export more, you designed the seam wrong. *Why:* legibility — you understand
   the door without the room.

4. **No leaky abstractions.** A third-party type or a lower layer's implementation
   detail must not leak across module boundaries. `sqlc` types stay in `store`;
   `bubbletea` types stay in `tui`. *Why:* a leak infects the whole codebase with a
   dependency's worldview and makes it unswappable and untestable.

5. **Prefer the elegant dependency, within the bar.** We are *not* dependency
   minimalists. Reach for the library that makes the code most legible — a nicer lib
   that reads better genuinely wins. But every dep clears a bar: readable surface,
   widely used, maintained, permissive licence, small transitive footprint, and *an
   agent can open its godoc and understand the seam*. Pin everything. Wrap only the
   risky/leaky ones behind a narrow interface. *Why:* legibility is #1, and good libs
   serve it — but an unvetted dep is code an agent must trust and can't see.

6. **Micro-libraries when — and only when — a pattern earns it.** When a pattern
   repeats, ask "is this its own small thing that does one job well?" Usually the
   answer is no; sometimes it's a clean yes (see the supervised-worker primitive).
   Extract *then*, not before. A micro-library graduates to its own module, not `pkg/`.

7. **Fail fast, and fail *helpful*.** Startup/config failures produce a clear,
   user-facing message and a non-zero exit — *"ANTHROPIC_API_KEY is required"*, not a
   panic stack trace. Missing config is a **user error** (helpful message), not a
   **programmer error** (panic). *Why:* don't get three minutes into a run to discover
   a missing key.

8. **Panics never escape a unit of work.** `panic` is reserved for genuine "can't
   happen" bugs. Every long-running unit (a run, a worker, the TUI loop) has a
   top-level recover boundary that logs the full chain, marks that unit failed, and
   keeps the process alive. A bad ticket kills its run and is *visible*; it never kills
   the factory. *Why:* correctness (halt the bad thing) **and** operability (stay alive
   and observable) — exactly the #2-over-#3 ordering.

9. **Observability is a platform feature, not a per-call chore.** Logging is baked
   into the primitives — the supervised worker logs its lifecycle, the stage-runner
   logs every stage, the LLM client logs every call. Leaf code rarely logs by hand.
   *Nobody can forget to log.* Logs are structured, verbose by default (debug on),
   carry a mandatory run-id, go to a **file** (never the terminal — the TUI owns it),
   and are **written for a future agent debugging the factory**. *Why:* "go check the
   logs" must actually yield the answer.

10. **The engine is headless.** The factory's logic knows nothing about the terminal.
    The TUI is a thin presentation layer that only (a) translates input → engine
    commands and (b) translates engine events → view state. All domain logic lives in
    the engine, driven via an `EventSink` seam. *Why:* ~95% of the system then tests as
    plain Go with no terminal, a headless run mode falls out nearly free, and "where's
    the logic?" always has one answer.

11. **Don't rely on reading.** Nothing critical may depend on an agent choosing to
    read a doc or skill. Climb the **enforcement pyramid** as high as you can:
    *mechanical (lint/type/codegen) > always-loaded context (`AGENTS.md`) > harness
    hooks > skills.* Skills are for *procedures*, never the only thing standing between
    an agent and a mistake that matters. *Why:* suggestions are optional; walls aren't.

12. **Single source of truth — in code as much as in docs.** Every fact, rule, and
    value has exactly one authoritative home; everything else *refers* to it. This is a
    general principle, and most of this guide is it in disguise: config lives only in
    `config`, time only behind `clock`, randomness behind one entropy seam, `sqlc` types
    only in `store`, the dependency graph in one composition root, each enum defined
    once, each lifecycle behind one transition guard, `AGENTS.md` canonical with
    `CLAUDE.md` a pointer. **A doc or comment is held to the same rule:** it must not
    restate what code, types, or a linter already express — don't list an enum's values
    in a comment, don't re-derive a signature in prose, don't copy a rule into two files.
    Copies are *guaranteed* to drift (two months later, nothing is in date). Corollaries:
    **cross-reference by name, not number** (a number rots the instant you insert above
    it); prefer a link over a copy; prefer generated over hand-maintained; if a
    hand-kept list *can* fall out of sync with what it describes, delete it and point at
    the thing. *Why:* the only thing that stays true is the thing with nothing to fall
    out of sync with. A confidently wrong doc is worse than no doc.

---

## Mechanical enforcement

Tenets that can be walls are walls. The wall is [`.golangci.yml`](../.golangci.yml)
plus CI plus harness hooks. Current stack:

| Concern | Enforced by |
|---|---|
| Architectural import boundaries (engine ⊥ TUI, sqlc in store, entropy in `cmd/`) | `depguard` |
| Banned calls (`os.Getenv`/`time.Now`/`fmt.Print` outside their one door) | `forbidigo` |
| No ignored errors | `errcheck` |
| Exhaustive enum switches | `exhaustive` |
| `context` not stored in structs / propagated correctly | `containedctx`, `contextcheck`, `fatcontext` |
| Doc comments on exported symbols & packages; comments end in a period | `revive`, `godot` |
| Ginkgo assertion hygiene | `ginkgolinter` |
| Known vulnerabilities in pinned deps | `govulncheck` (pre-push + CI) |
| Data races | `go test -race` (pre-push + CI) |
| General correctness net | `staticcheck` + friends |
| Lint + tests before "done" | `lefthook` git hooks (+ harness hooks later) |

The judgment-tier standards that a linter *can't* catch — narrow-door, no-leaky, units-at-end, parse-don't-validate, comment/test-name quality — are caught by the
[`review-changes`](../skills/review-changes/) skill, the review rung above the wall.

If you find yourself wanting to disable one of these, the answer is to **fix the
code**, not the config (tenet #2).

---

## Construction

- **Parse, don't validate.** Convert untrusted/external input (DB rows, API payloads,
  ticket bodies, user input) into a **typed domain value at the boundary**. Inside the
  engine, data is valid *by its type* and is never re-checked.
- **Required deps are positional constructor args; optional config uses functional
  options.** Required dependencies can't be omitted (the compiler enforces it); `With…`
  options tune things that have safe defaults. `New` sets defaults, applies options,
  then validates the resolved combo once (fail-fast, helpful). The `options` struct is
  unexported. See [ADR-0018].
- **No usable-but-invalid zero value.** If a type needs setup, give it unexported fields
  and a `New` constructor (like `id`, options) — never a zero value that is
  half-constructed but callable.

---

## Concurrency & context

- **`context.Context` is the first parameter** of anything doing I/O or long-running
  work, propagated everywhere, **never stored in a struct**, cancellation always
  honored. No `context.TODO()` in production.
- **All long-running work goes through the supervised-worker primitive** ([ADR-0008]) —
  never a raw `go func`.
- **`go test -race` always.** Prefer clear goroutine ownership and channels over shared
  mutable state; shared mutable state needs a documented mutex. See [ADR-0019].

---

## Interfaces & packages

- **Interfaces are defined consumer-side and kept small** — declared where they're
  *used*, holding only the methods that consumer needs. *Accept interfaces, return
  concrete types.* This is what produces the narrow-door/deep-module shape (tenet #3).
- **No grab-bag packages.** `util`, `common`, `helpers`, `misc`, `shared` are banned —
  they are where cohesion goes to die. A thing belongs in the domain it serves, or is
  its own well-named micro-library (tenet #6).

---

## Identity & IDs

- **Stripe-style IDs: `<prefix>_<ulid>`** (e.g. `run_01J9Z3QK8X…`). The prefix makes an
  ID self-describing on sight (legibility/operability); the ULID body sorts by creation.
- **Minted by an injected `id.Generator`** bundling the clock + an entropy `io.Reader` —
  both non-deterministic edges controlled, so IDs are sortable *and* reproducible in
  tests. Randomness is injected; `crypto/rand`/`math/rand` are banned outside `cmd/`.
- **Each entity has an un-forgeable typed ID** — a struct with an unexported field, so
  the only ways to obtain one are `NewXxxID(gen)` or a validated `ParseXxxID(s)`. The
  compiler stops you passing one entity's ID for another's. See [ADR-0017].

---

## Time & units

- **The engine is UTC-only.** Every `time.Time` in the engine is UTC. **Only the TUI
  localizes** to the user's zone — the same engine/presentation boundary as tenet #10.
  Enforced: `time.Now` and `time.Local` are banned outside `internal/clock`, and
  `clock.Clock` *returns* UTC, so a local-zone timestamp cannot enter the engine.
- **Read time only through the injected `clock.Clock`.** Never `time.Now()` directly
  (testability floor). See [ADR-0016].
- **No sleeps in tests.** A retry/backoff test advances a *fake* clock; it never waits.
  When the first retry is built, the clock seam grows injectable timers (deferred choice:
  hand-roll vs `clockwork`).
- **Serialize timestamps as RFC3339 / ISO-8601 UTC text** (readable and sortable in
  sqlite, logs, JSON).
- **Relative time ("5m ago")** is a TUI helper computed from the injected clock, used
  when it aids the reader — not everywhere.
- **Units-at-end-of-names**, with the Go nuance: a `time.Duration` carries its unit in
  the *type* — do **not** suffix it. A raw number that carries a unit but isn't a typed
  unit gets a camelCase unit suffix: `sizeBytes`, `maxAgeSeconds`, `retryDelayMS`.
  (Judgment-tier — review, not lint.)

---

## Code prose

- **Every exported symbol and every package has a doc comment**, starting with the
  symbol/package name, full sentence, ending with a period (Go idiom; `revive` +
  `godot` enforce it).
- **Comment the *why*, not the *what*.** A comment restating the code is banned noise;
  comments carry intent, invariants, and gotchas — the non-obvious.
- **No commented-out code. Scoped `TODO(scope):` only** — never a bare `// TODO`.
- **Test names read as behaviour sentences.** Ginkgo `Describe`/`Context`/`It`
  concatenate into English: `It("rejects an id parsed under the wrong prefix")`.
  Present tense, one behaviour per `It`, no "should"/"test"/"correctly".

---

## Pointers

- Values & tenets: **this document.**
- Architectural decisions and their rationale: [`docs/adr/`](./adr/).
- Always-loaded agent context & operating protocol: [`../AGENTS.md`](../AGENTS.md).
- Procedures (how to add a domain package, a migration, a worker): [`../skills/`](../skills/).
- Reviewing new code against the judgment-tier standards: [`../skills/review-changes/`](../skills/review-changes/).
- The wall: [`../.golangci.yml`](../.golangci.yml).
