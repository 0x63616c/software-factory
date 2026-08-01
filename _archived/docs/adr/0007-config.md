# ADR-0007: Configuration — one typed struct, validated loudly, koanf

- Status: Accepted
- Date: 2026-07-23

## Context
Nothing rots legibility like `os.Getenv` buried deep in some random file — you can never
find all the config or tell what's actually used. A software factory will also have
genuinely *structured* config (target repos, per-stage model choices, pipeline settings),
and it must fail fast and *helpfully* when misconfigured, not three minutes into a run.

## Decision
- **One typed `Config` struct.** Everything the factory needs is a typed field. No
  stringly-typed `Get("some.key")` scattered around.
- **One `Validate()` method, called once at startup**, returning a *helpful aggregated*
  error listing every problem at once, then a clean non-zero exit — never a panic dump.
  Validation is ours and explicit: an agent reads one function to see every requirement.
  (This is the fail-fast-*helpful* rule from [ADR-0006].)
- **Secrets come from env, never a committed file.** The config *file* holds non-secret
  structure only.
- **`koanf`, multi-source**, merged with precedence **flags > env > file > defaults**,
  unmarshalled into the one typed struct.
- **`os.Getenv` / `os.LookupEnv` are banned outside `internal/config`**, enforced by
  `forbidigo` with a path exclusion for the config package. The single answer to "what
  config exists?" is: read the `Config` struct.
- **Flags resolve in `main` before the TUI starts** (`parse flags/env/file → build Config
  → Validate() → launch bubbletea`), so flags never touch the event loop and don't
  conflict with the TUI. Flags stay useful on a TUI (`--config`, `--log-level`, a future
  headless subcommand).

## Rejected alternatives
- **`spf13/viper`.** Rejected: heavy, magic, force-lowercases keys, large dependency
  tree — fails the legibility bar for dependencies.
- **`go-envconfig` (env-only).** Viable and its test lookupers are nice, but env-only
  gets ugly once config is structured/nested; a factory wants a file. Kept as the
  "start minimal" fallback if we ever regret the file.
- **Library `required`-tag validation.** Preferred an explicit `Validate()` — more
  legible and more mechanical than scattered magic tags; one function shows every rule.

## Consequences
- Config becomes a deep module ([ADR-0005]) with a narrow surface: the typed `Config` and
  its `Validate()`.
- `cobra` is deliberately not pulled until real subcommands exist; stdlib `flag` feeds
  koanf.
- The `os.Getenv` ban is a concrete instance of "make correctness mechanical" — the
  disease is cured at the linter, not by discipline.
