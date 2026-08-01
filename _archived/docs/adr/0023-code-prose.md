# ADR-0023: Code prose — comments, docstrings, and test names

- Status: Accepted
- Date: 2026-07-23

## Context
Prose in code — doc comments, inline comments, test descriptions — is read far more than
it's written, and by agents as much as humans. Left unstandardized it drifts into noise
(comments restating code) or silence (undocumented exports, cryptic test names). Since
this codebase is maintained by agents, legible prose is part of legibility (the top-ranked value).

## Decision

### Comments & docstrings
- **Every exported symbol has a doc comment starting with its name**, a full sentence
  ending in a period. Go idiom; enforced by `revive` `exported` + `godot`.
- **Every package has a package doc** stating its one job and its narrow door. Enforced
  by `revive` `package-comments`.
- **Comment the WHY, not the WHAT.** Code already says what it does; a comment that
  restates the code is banned noise. Comments carry intent, invariants, gotchas — the
  things a cold reader cannot infer.
- **Prefer a type or a test over a comment for an invariant** — "must be UTC" as a
  comment is weaker than a clock that *returns* UTC. Make-correctness-mechanical (tenet #1)
  beats a note.
- **No commented-out code.** Dead code rots and is escape-hatch-adjacent; delete it (git
  remembers).
- **`TODO(scope):` always scoped and greppable** — e.g. `TODO(runtime-spine)`. Never a
  bare `// TODO`.
- **Written for a future agent debugging this** — state assumptions and what breaks the
  thing, same spirit as logs ([ADR-0009]).

### Test names (ginkgo)
Ginkgo's `Describe`/`Context`/`It` strings concatenate into a sentence — so they must
read as one:
- **`Describe("<unit under test>")`** — the type/func name (`"Generator"`, `"Fake"`).
- **`Context("when <condition>")`** — the precondition, optional.
- **`It("<behaviour, present tense, observable outcome>")`** — one behaviour, present
  tense, **no "should", no "test", no "correctly"**: `"rejects an id parsed under the
  wrong prefix"`. Reads as *"Generator → rejects an id parsed under the wrong prefix ✓"*.
- One assertion-of-behaviour per `It`; parametric cases use `DescribeTable`/`Entry`.
  Suite bootstrap: `RunSpecs(t, "<Package> Suite")`. `ginkgolinter` checks assertion
  hygiene (not prose).

## Rejected alternatives
- **Comments that restate code** — noise that rots out of sync with the code.
- **Undocumented exports / packages** — an agent can't understand a door with no label.
- **Test names like `TestFoo_Case1` or `It("should work")`** — say nothing about
  behaviour; the output stops being a readable spec.

## Consequences
- Mechanical part = `revive` (exported, package-comments) + `godot` + `ginkgolinter`.
- Judgment part (why-not-what, no dead code, sentence test names) = the review skill.
