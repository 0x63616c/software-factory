# Writing scalable TypeScript

This guide governs the active console under `web/`. The Go standards do not.

Keep the compiler strict and treat every external value as `unknown` until a
boundary parser validates it. Model domain states as discriminated unions so
invalid combinations are unrepresentable; use branded types when two strings
have different domain meaning. Expected failure is data returned in a typed
result, while thrown exceptions are reserved for bugs and unavailable
infrastructure.

Prefer local mutation inside a small function over allocations that obscure the
algorithm, but do not expose mutable shared state. Centralize domain strings,
registries, API routes, and generated contracts; one fact gets one source of
truth. Comments explain constraints and rejected alternatives, not syntax.

Enforce architecture mechanically where possible. `tsc`, Biome, generated
client drift checks, Vitest, and the dependency boundaries in this repository
are part of the design. Do not suppress them. Generated API code is refreshed
through `scripts/regenerate.sh`, never edited by hand.

For UI work, keep server state in TanStack Query, use the existing components
and visual language, and add tests around behavior rather than implementation
details. No placeholder data belongs in a production path.
