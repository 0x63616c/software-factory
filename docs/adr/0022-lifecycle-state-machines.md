# ADR-0022: Lifecycles are explicit typed state machines

- Status: Accepted
- Date: 2026-07-23

## Context
The factory moves work through states — a ticket goes something like intake → in-progress
→ in-review → merged, a run has its own lifecycle. Modeled as `string` or `bool` status
fields, every illegal value is assignable, exhaustiveness is impossible, and an invalid
transition (merged → intake) applies silently. That is precisely the class of bug our
make-illegal-states-unrepresentable stance exists to kill. (Concrete state/entity names
are deferred — domain naming is TBD.)

## Decision
Model each entity's lifecycle as an explicit typed state machine:

- **States are a typed enum** (a defined type, not `string`), so only declared states are
  representable and `exhaustive` ([the lint stack]) forces every switch to handle them all.
- **Transitions go through one function** — a single guard (`func (s State) To(next State)
  (State, error)` or equivalent) that permits only legal moves and returns an error on an
  illegal one. There is no other way to change state.

So an invalid state is unrepresentable and an invalid transition is *rejected at one
guard*, never applied silently.

## Rejected alternatives
- **`string`/`bool` status fields.** Any value assignable, no exhaustiveness, illegal
  transitions silent — the exact disease.
- **A typed enum with no transition guard.** Fixes representability but still lets
  `state = Merged` be assigned from anywhere; illegal moves remain possible.
- **Transition logic scattered across call sites.** No single place to trust; the rules
  drift and contradict. One guard, one source of truth.

## Consequences
- Adding a state is a compile error everywhere it must be handled (`exhaustive`), so the
  machine can't silently ignore it.
- The transition guard is the single, testable definition of the workflow — the natural
  home for logging each move ([ADR-0009]).
- Ties directly to [ADR-0017] (typed IDs) and [ADR-0018] (construct-valid-or-not-at-all):
  entities are valid by type, in a valid state, from birth.
