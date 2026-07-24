# ADR-0020: Secrets are typed and self-redacting

- Status: Accepted
- Date: 2026-07-23

## Context
Secrets (API keys, tokens) enter via env through `internal/config` ([ADR-0007]), never a
committed file (`.env` is gitignored). But logging is verbose-by-default and written for
a future debugging agent ([ADR-0009]) — so a secret held as a bare `string` leaks the
moment anything logs the struct it lives in, or prints it in an error. "Remember not to
log the key" is exactly the rely-on-reading failure we reject ([ADR-0013]).

## Decision
Every secret is wrapped in a dedicated type that **masks itself** everywhere it could be
rendered — `String()`, `slog.LogValue()`, `MarshalJSON`/`MarshalText` all return `"***"`
(or `"redacted"`). The real value is reachable only through an explicit accessor
(e.g. `.Reveal()`), used solely at the point of use (the HTTP client, the CLI arg). So
even if the containing struct is logged, JSON-encoded, or `fmt`-printed, the secret
cannot leak — masking is structural, not a discipline. Secrets are never persisted to the
store.

## Rejected alternatives
- **Bare `string` secrets.** Leak the instant they're logged, printed, or serialized —
  and verbose-logging makes that likely, not hypothetical.
- **A lint rule "don't log secrets".** Can't reliably detect "this string is a secret";
  relies on the author knowing. The self-redacting type makes leaking structurally hard
  instead (make-correctness-mechanical).
- **Manual redaction at each log site.** N places to get right, one to forget.

## Consequences
- The masking type is the single place secret-rendering is defined; add a new sink
  (a new marshaler) and every secret is covered.
- `.Reveal()` calls are greppable — an audit of "where is the real key used" is a search,
  not an investigation.
- Config validation ([ADR-0007]) still fails fast+helpful on a missing secret without
  ever printing its value.
