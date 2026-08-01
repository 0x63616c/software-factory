# SoftwareStyle

This project's own style guide — the *idea* of having one is borrowed from TigerBeetle's
TigerStyle; the content is ours and deliberately different. **SoftwareStyle optimizes a
software factory written and maintained by agents, for agents.** Correctness comes from
*types, linters, and tests* at compile-time and CI — not runtime assertions.

The north star. Rule vs this doc → this doc wins. Rule vs rule → the priority ordering
decides. The *why* behind each structural choice lives as an ADR in [`docs/adr/`](./adr/);
this doc holds values and tenets. Read both.

---

## The one idea everything hangs off

This repo is a **software factory**: a TUI that takes tickets and ships code to
production. It's built to operate on *any* codebase — Go, Python, whatever — and one of
them is **itself**. That forces a split running through the whole design:

- **Layer A — the factory's own code.** The Go that *is* the factory. This doc.
- **Layer B — the standards the factory applies to what it builds.** Owned by each target
  project, not us. Per-target, pluggable data.

**Same mechanism, different content.** The factory reads a target's standards as *data*
(`AGENTS.md` + `skills/` + lint config in the repo). Our standards live in those same
files — so **this repo is the factory's first target**, and self-hosting is nearly free.

Load-bearing consequence: **the engine is language-agnostic. All language/standards
knowledge is data (the standards bundle), never baked into engine code.**

---

## The priority ordering

Resolve every trade-off in this order. Higher beats lower.

> **Legibility > Correctness > Operability > Economy**

Machine performance (cache-friendliness, allocation, sub-microsecond) is **not on the
list** — this is a human-scale, LLM-latency-bound system. Below ~1s, don't care.

### Legibility — *understandable in isolation?*
Understand a piece **without loading the whole system**, and verify you got it right.
- **Test:** open one file cold — can you tell what it does, assumes, and breaks on,
  without opening five others?
- **In code:** deep modules, narrow interfaces, intent-revealing names, invariants
  encoded in types, no hidden global state.

### Correctness — *right thing, loud on failure?*
Does what it's specified to; when it can't, fails **loud and early**, never limps.
- **Test:** on bad input or broken invariant, does it halt with a clear error or produce
  silent garbage?
- **In code:** parse-don't-validate at boundaries, illegal states unrepresentable, no
  empty catch, no default-to-zero that hides a bug.
- **Scope:** means *the factory program behaves* (ticket state intact, pipeline moves as
  designed). Says **nothing** about the code the factory emits — that's Layer B.

### Operability — *see it and stop it while it runs?*
Running autonomously, you can **see** and **stop/reverse** it.
- **Test:** mid-run at 3am — can you tell which stage, why it chose what it did, and kill
  it without corrupting state?
- **In code:** structured logs at decisions, observable state, idempotent/reversible
  steps, clean cancellation.

### Economy — *LLM spend + human wait.*
The bill and the wait. **Architectural, not micro-optimization** — cut it with
fewer/cheaper calls, caching, batching, model choice, not tight loops. The **tiebreaker**
among equal designs: pay tokens for a correctness/operability win, never for a third
redundant pass that buys nothing.

### The trades
- **Legibility > Correctness.** Split a clever function into three obvious ones, or add a
  typed wrapper that *can't* be misused. An agent maintains this code; what it can't
  understand it can't safely change, so illegible-but-correct rots to incorrect on the
  next edit.
- **Correctness > Operability.** Broken invariant mid-run: halt it. "Log and keep going"
  produces confident garbage.
- **Operability > Economy.** Decision logs and idempotent steps cost tokens. Pay them —
  you can't run an autonomous code-shipping system you can't watch and stop.

---

## Testability is a floor, not a dial

Not ranked among the four — a **non-negotiable property**, never traded. It's the
substrate of the agentic loop (*change → test → read → decide*); untestable code is a
blind agent guessing.

**The rule:** *no unit test touches the real world.* Every external edge — LLM, shell,
clock, filesystem, network, terminal — sits behind a narrow injectable interface, so a
test hands it a fake. Deterministic: injected clock, no real time/randomness/network/model.

In-memory sqlite (`:memory:`) is **not** the real world — hermetic, deterministic, fresh
per test. Prefer it to mocking the store. This is *why* we use manual constructor
injection ([ADR-0004]): a test hands a fake to a constructor with zero magic.

---

## The tenets

Rule, *why*, and — where possible — how it's enforced. See *Mechanical enforcement*.

1. **Make correctness mechanical.** If a linter, codegen, or the type system can enforce
   an invariant, it **must** — never left to discipline. Codegen welcome. *Why:* agents
   forget; walls don't. Strongest lever we have.

2. **No escape hatches.** Never silence a tool — fix what it flags. Banned: `//nolint`,
   ignored errors, bare `any`, silent `recover`. *Why:* silencing hides real defects.

3. **Deep modules, narrow door.** Small door, big room — narrow stable public surface,
   deep private internals. Growth goes *down* into private sub-packages, never *out* into
   the surface. If a change forces you to export more, the seam is wrong. *Why:*
   legibility — understand the door without the room.

4. **No leaky abstractions.** A third-party type or lower-layer detail must not cross
   module boundaries. `sqlc` stays in `store`; `bubbletea` stays in `tui`. *Why:* a leak
   infects the codebase with a dependency's worldview and kills swappability + testing.

5. **Prefer the elegant dependency, within the bar.** Not minimalists — reach for the lib
   that reads best. Bar: readable surface, widely used, maintained, permissive licence,
   small transitive footprint, godoc-legible seam. Pin everything. Wrap only risky/leaky
   ones. *Why:* legibility is #1 and good libs serve it; an unvetted dep is code an agent
   must trust but can't see.

6. **Micro-libraries only when a pattern earns it.** Repeating pattern? Ask "its own
   small thing that does one job well?" Usually no; sometimes a clean yes (the
   supervised-worker). Extract *then*, not before — and to its own module, not `pkg/`.

7. **Fail fast, fail *helpful*.** Startup/config failure → clear user-facing message +
   non-zero exit (*"ANTHROPIC_API_KEY required"*), never a panic dump. Missing config is
   a user error, not a programmer bug. *Why:* don't discover a missing key three minutes
   into a run.

8. **Panics never escape a unit of work.** `panic` is for genuine "can't happen" bugs.
   Every long-running unit (run, worker, TUI loop) has a top-level recover boundary that
   logs the chain, marks the unit failed, and keeps the process alive. A bad ticket kills
   its run *visibly*, never the factory. *Why:* correctness (halt the bad thing) **and**
   operability (stay alive and observable).

9. **Observability is a platform feature, not a per-call chore.** Logging is baked into
   the primitives — the supervised worker, stage-runner, and LLM client log themselves.
   Leaf code rarely logs by hand; *nobody can forget*. Structured, verbose by default,
   run-id-correlated, to a **file** (never the terminal — the TUI owns it), **written for
   a future agent debugging the factory**. *Why:* "check the logs" must yield the answer.

10. **The engine is headless.** Factory logic knows nothing about the terminal. The TUI
    only translates input → engine commands and engine events → view state; all logic
    lives in the engine, driven via an `EventSink` seam. *Why:* ~95% tests as plain Go, a
    headless run mode is near-free, and "where's the logic?" has one answer.

11. **Don't rely on reading.** Nothing critical may depend on an agent *choosing* to read
    a doc. Climb the **enforcement pyramid**: *mechanical (lint/type/codegen) >
    always-loaded context (`AGENTS.md`) > harness hooks > skills*. Skills are for
    procedures, never the only guard against a mistake that matters. *Why:* suggestions
    are optional; walls aren't.

12. **Single source of truth — code as much as docs.** Every fact, rule, and value has
    one authoritative home; everything else *refers* to it. Most of this guide is this in
    disguise: config only in `config`, time only behind `clock`, randomness behind one
    entropy seam, `sqlc` only in `store`, one composition root, each enum defined once,
    each lifecycle behind one transition guard, `AGENTS.md` canonical with `CLAUDE.md` a
    pointer. **Docs held to the same rule:** never restate what code, types, or a linter
    already express — no enum values in a comment, no signature re-derived in prose, no
    rule copied into two files. Copies drift (two months on, nothing's in date).
    Corollaries: **cross-reference by name, not number** (numbers rot on insert); link
    don't copy; generated over hand-maintained; a hand-kept list that *can* desync gets
    deleted and pointed at the source. *Why:* the only thing that stays true has nothing
    to desync with. A confidently wrong doc is worse than none.

---

## Mechanical enforcement

Tenets that can be walls are walls: [`.golangci.yml`](../.golangci.yml) + CI + hooks.

| Concern | Enforced by |
|---|---|
| Import boundaries (engine ⊥ TUI, sqlc in `store`, entropy in `cmd/`) | `depguard` |
| Banned calls (`os.Getenv`/`time.Now`/`fmt.Print` outside their one door) | `forbidigo` |
| Ignored errors | `errcheck` |
| Exhaustive enum switches | `exhaustive` |
| `context` not struct-stored / propagated right | `containedctx`, `contextcheck`, `fatcontext` |
| Doc comments on exports & packages; comments end in a period | `revive`, `godot` |
| Ginkgo assertion hygiene | `ginkgolinter` |
| Known vulnerabilities in pinned deps | `govulncheck` (pre-push + CI) |
| Data races | `go test -race` (pre-push + CI) |
| General correctness net | `staticcheck` + friends |
| Lint + tests before "done" | `lefthook` git hooks |

Judgment-tier standards a linter *can't* catch — narrow-door, no-leaky, units-at-end,
parse-don't-validate, comment/test-name quality — are caught by the
[`review-changes`](../skills/review-changes/) skill, the rung above the wall.

Want a rule off? Fix the code, not the config (tenet 2).

---

## Construction

- **Parse, don't validate.** Turn untrusted/external input (DB rows, API payloads, ticket
  bodies, user input) into a **typed domain value at the boundary**. Inside, data is valid
  *by type* and never re-checked.
- **Required deps positional; optional config via functional options.** Required deps
  can't be omitted (compiler-enforced); `With…` options tune things with safe defaults.
  `New` sets defaults, applies options, then validates once (fail-fast, helpful). The
  `options` struct is unexported. See [ADR-0018].
- **No usable-but-invalid zero value.** Needs setup? Unexported fields + a `New`
  constructor (like `id`, options) — never a half-built callable zero value.

---

## Concurrency & context

- **`context.Context` is the first param** of anything doing I/O or long-running work;
  propagated everywhere, **never struct-stored**, cancellation always honored. No
  `context.TODO()` in production.
- **All long-running work goes through the supervised-worker primitive** ([ADR-0008]) —
  never a raw `go func`.
- **`go test -race` always.** Prefer goroutine ownership + channels over shared mutable
  state; shared mutable state needs a documented mutex. See [ADR-0019].

---

## Interfaces & packages

- **Interfaces are consumer-side and small** — declared where used, only the methods that
  consumer needs. *Accept interfaces, return concrete types.* This produces the
  narrow-door shape (tenet 3).
- **No grab-bag packages.** `util`, `common`, `helpers`, `misc`, `shared` — banned; where
  cohesion dies. A thing belongs to the domain it serves, or is its own named
  micro-library (tenet 6).

---

## Identity & IDs

- **Stripe-style IDs: `<prefix>_<ulid>`** (e.g. `run_01J9Z3QK8X…`). The prefix makes an ID
  self-describing on sight; the ULID body sorts by creation.
- **Minted by an injected `id.Generator`** bundling clock + entropy `io.Reader` — both
  edges controlled, so IDs sort *and* reproduce in tests. `crypto/rand`/`math/rand` banned
  outside `cmd/`.
- **Un-forgeable typed ID per entity** — a struct with an unexported field; the only ways
  in are `NewXxxID(gen)` or validated `ParseXxxID(s)`. The compiler stops cross-passing
  one entity's ID for another. See [ADR-0017].

---

## Time & units

- **Engine is UTC-only.** Every engine `time.Time` is UTC; **only the TUI localizes**.
  Enforced: `time.Now`/`time.Local` banned outside `clock`, and `clock.Clock` *returns*
  UTC — a local timestamp can't enter the engine. See [ADR-0016].
- **Read time only through injected `clock.Clock`** — never `time.Now()` (testability).
- **No sleeps in tests.** Retry/backoff advances a *fake* clock, never waits. Injectable
  timers arrive with the first retry (deferred: hand-roll vs `clockwork`).
- **Serialize timestamps as RFC3339 UTC** (readable + sortable in sqlite/logs/JSON).
- **Relative time ("5m ago")** — a TUI helper from the injected clock, when it aids.
- **Units-at-end-of-names**, Go nuance: a `time.Duration` carries its unit in the type —
  don't suffix it. A raw unit-bearing number gets a camelCase suffix: `sizeBytes`,
  `maxAgeSeconds`, `retryDelayMS`. (Judgment-tier — review.)

---

## Code prose

- **Every exported symbol and package has a doc comment** starting with its name, full
  sentence, ending in a period (`revive` + `godot`).
- **Comment the *why*, not the *what*.** A comment restating code is banned noise;
  comments carry intent, invariants, gotchas — the non-obvious.
- **Short but sweet.** Fewest words that keep the meaning. Cut filler (*just, really,
  simply, actually*), redundant qualifiers, restated context. Half the length, same
  meaning → do it. Brevity *is* legibility, and costs fewer tokens on every load.
- **No commented-out code. Scoped `TODO(scope):` only** — never a bare `// TODO`.
- **Test names read as behaviour sentences.** Ginkgo `Describe`/`Context`/`It` concatenate
  into English: `It("rejects an id parsed under the wrong prefix")`. Present tense, one
  behaviour per `It`, no "should"/"test"/"correctly".

---

## Pointers

- Values & tenets: this doc.
- Decisions + rationale: [`docs/adr/`](./adr/).
- Always-loaded context + operating protocol: [`../AGENTS.md`](../AGENTS.md).
- Procedures: [`../skills/`](../skills/).
- Judgment-tier review: [`../skills/review-changes/`](../skills/review-changes/).
- The wall: [`../.golangci.yml`](../.golangci.yml).
