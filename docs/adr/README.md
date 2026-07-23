# Architecture Decision Records

Each ADR records one decision: the problem, what we chose, **what we rejected and
why**, and the consequences. The rejected-alternatives section is the point — it's the
insurance against future-us finding a seam and asking "why did we do that?".

Values and tenets live in [../SoftwareStyle.md](../SoftwareStyle.md); the decisions
that fall out of them live here. Format is fixed: Context / Decision / Rejected
alternatives / Consequences. Status is one of Proposed / Accepted / Superseded.

## Index

1. [Priority ordering](0001-priority-ordering.md) — Legibility > Correctness > Operability > Economy
2. [Testability is a floor](0002-testability-is-a-floor.md) — never traded; no unit test touches the real world
3. [Language-agnostic engine / two layers](0003-language-agnostic-engine-two-layers.md) — standards are data; this repo is the factory's first target
4. [Manual constructor injection](0004-manual-constructor-injection.md) — one composition root; no runtime DI framework
5. [Repo shape by domain](0005-repo-shape-by-domain.md) — single module/binary, deep modules, nested `internal/`
6. [Error handling](0006-error-handling.md) — cockroachdb/errors, wrap-up, fail-fast-helpful, recover boundary per unit
7. [Config](0007-config.md) — koanf, one typed `Config` + `Validate()`, `os.Getenv` sealed in `config`
8. [Supervised-worker primitive](0008-supervised-worker-primitive.md) — our own; actor frameworks rejected
9. [Logging is a platform feature](0009-logging-is-a-platform-feature.md) — baked into primitives, to a file, explicit injection
10. [Persistence](0010-persistence.md) — modernc sqlite, goose, sqlc sealed in `store`, test on `:memory:`
11. [Headless engine + EventSink](0011-headless-engine-eventsink.md) — thin TUI, daemon deferred behind the seam
12. [Testing tiers & LLM](0012-testing-tiers-and-llm.md) — unit/integration/e2e; fake/cassette/live
13. [Enforcement pyramid](0013-enforcement-pyramid.md) — don't rely on reading; mechanical > context > hooks > skills
14. [Standards as a loadable bundle](0014-standards-as-loadable-bundle.md) — conventional files; `AGENTS.md` canonical
15. [Agent operating protocol](0015-agent-operating-protocol.md) — branch/PR, done-loop, TDD mandatory for engine
16. [Time & units](0016-time-and-units.md) — UTC-only engine, RFC3339 on the wire, units in names
17. [Identifiers](0017-identifiers.md) — Stripe-style, ULID-backed, typed IDs via a Generator
18. [Construction](0018-construction.md) — required deps positional, optional config via functional options
19. [Context & concurrency](0019-context-and-concurrency.md) — ctx-first, never stored, `-race`, supervised workers only
20. [Secret redaction](0020-secret-redaction.md) — secrets are masked types, never logged, never persisted
21. [Shell-out discipline](0021-shell-out-discipline.md) — external commands wrapped, argv not shell, cancellable
22. [Lifecycle state machines](0022-lifecycle-state-machines.md) — typed states, one guarded transition
23. [Code prose](0023-code-prose.md) — comments, docstrings, and test names
24. [Boundaries & interfaces](0024-boundaries-and-interfaces.md) — parse don't validate, consumer-side interfaces, no grab-bag packages

*Deferred — to be specified with the runtime spine, not yet decided: the LLM interaction
layer (prompts versioned & embedded, output validated at the boundary, model-per-stage),
per-run token/cost accounting (the Economy axis made real), and idempotency/resumability
of side-effecting steps (commits, PRs).*
