# ADR-0024: Parse don't validate, consumer-side interfaces, no grab-bag packages

- Status: Accepted
- Date: 2026-07-23

## Context
Three related legibility/correctness rules about *shape*: how untrusted data enters the
system, where interfaces are defined, and how packages are carved. Each has a wrong
default that Go makes easy, so we state the right one.

## Decision

### Parse, don't validate
External/untrusted input — DB rows, API payloads, ticket bodies, user input — is
converted into a **typed domain value at the boundary**, and once inside the engine it is
**valid by its type**, never re-checked. `ParseXxxID` ([ADR-0017]) is the archetype:
outside is `string`, the boundary either returns a valid typed value or an error, and
downstream code receives only valid values. This extends illegal-states-unrepresentable
to every input edge and kills the "did we validate this yet?" question.

### Interfaces: consumer-side and small
An interface is defined **where it is used, not where it is implemented**, and contains
**only the methods that consumer needs.** "Accept interfaces, return concrete types."
This is the Go idiom that *produces* the deep-module / narrow-door shape (tenet #3): the
consumer states a minimal need, the producer returns a concrete type, and nobody defines
a fat producer-side interface that everything must satisfy.

### No grab-bag packages
No `util`, `common`, `helpers`, `misc`, or `shared` packages. They have no single job,
so they accrete unrelated code and become the opposite of a deep module. Code belongs in
the domain package whose job it serves; a genuinely reusable one-job thing becomes its
own named micro-library ([ADR: dependency stance]).

## Rejected alternatives
- **Validate-and-pass-strings** — data stays stringly-typed and every consumer must
  re-validate (or forget to); the type never guarantees validity.
- **Producer-side fat interfaces** — the consumer depends on methods it doesn't use,
  coupling widens, and the "narrow door" is lost.
- **A `util` package "just for now"** — it never stays small; it's where cohesion goes to
  die.

## Consequences
- Boundary parsing concentrates validation at the edges, keeping the interior total.
- Consumer-side interfaces keep mocks small and dependencies honest (testability floor).
- The grab-bag ban is judgment-tier (a review-skill check) plus obvious in review.
