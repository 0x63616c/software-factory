# ADR-0018: Required deps positional, optional config via functional options

- Status: Accepted
- Date: 2026-07-23

## Context
Components need dependencies (required) and tuning (optional, with sane defaults). Two
failure modes to avoid: a giant positional constructor where half the args are optional
noise, and an all-options constructor where a *required* dependency can be silently
omitted, producing a half-built, invalid object.

## Decision
Split construction by necessity:

- **Required dependencies → positional constructor arguments.** The things a component
  cannot function without (`clock`, `store`, `logger`) are mandatory positional params,
  so the **compiler forces you to pass them.** You cannot construct the object without them.
- **Optional configuration → functional options** (`With…`). Tuning with a safe default
  (timeouts, retry counts, buffer sizes, toggles). Readable call sites, and a new option
  can be added later without breaking a single caller.

```go
func New(clk clock.Clock, store Store, opts ...Option) (*Service, error) {
	o := options{timeout: 30 * time.Second, retries: 3} // defaults first
	for _, opt := range opts { opt(&o) }
	// validate the resolved combo ONCE, fail-fast + helpful (ADR-0006)
	if o.retries < 0 { return nil, errors.Newf("retries must be >= 0, got %d", o.retries) }
	return &Service{clk: clk, store: store, timeout: o.timeout, retries: o.retries}, nil
}
```

Sub-rules:
- **The `options` struct is unexported**; the only way to set fields is the `With…`
  funcs, so callers can't bypass defaults/validation (same un-forgeable spirit as [ADR-0017]).
- **Options are infallible setters; validation happens once in the constructor**
  (fail-fast, aggregated), mirroring `Config.Validate()` ([ADR-0007]). Don't scatter
  validation into each `With…`.
- **No usable-but-invalid zero value.** If a type needs setup to be valid, its fields are
  unexported and it is built by `New`. Never leave a type where the zero value compiles
  but is half-valid; construct it, don't zero-init it into a broken state.

## Rejected alternatives
- **Required deps as options** (`New(WithClock(c))`) — a required dependency could be
  omitted, yielding an invalid object. Violates illegal-states-unrepresentable.
- **A big config struct passed by value** — every field looks optional, no defaults
  story, and adding a field silently zero-values it at every call site.
- **Fallible per-option validation** — scatters the fail-fast check; one aggregated
  validation in the constructor is more legible and gives one helpful error.

## Consequences
- Functional options are the number-one pattern *for the optional half* of construction,
  never a way to smuggle in a required dependency.
- "Use an option vs a positional arg" is judgment-tier → the review skill checks it.
