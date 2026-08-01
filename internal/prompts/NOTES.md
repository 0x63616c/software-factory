# Stage prompts — design rationale

Four files: `base.md` plus one per stage (`plan`, `implement`, `review`). `base.md` is
prefixed to every stage prompt; the stage file is the suffix.

**Rewritten for #435's pipeline rewrite.** The pipeline was `plan → review → revise →
implement → propose`, five stages run once each, in order. It is now `plan → implement →
review`, and it loops: a red CI run sends `implement` around again in the same window, and a
blocking review finding opens a fresh `implement` window afterward, under the turn budgets
`internal/workflows`' loop enforces. `revise` and `propose` are gone, as stages and as words:
`revise`'s job folded into `implement` (a plan an implementer finds wrong is a plan it
deviates from, in its own report, not a fourth stage's document), and `propose`'s job —
opening the pull request — is now workflow code acting on GitHub's API, driven by `implement`'s
own `title`/`body` fields, rather than a model told to run `gh pr create`. What survives from
the original design below: the untrusted-issue fence, the handoff-document fence, and the
overall assembly order. What's new: `review`'s structured `findings` (with a stable `id`
sameness is judged on), `implement`'s resumed multi-turn conversation, and both stages'
"declare absence rather than omit" handling of documents that don't exist yet on a turn one.

## Assembly order

```
base.md                      role, contract, "nobody to ask", the untrusted issue fence
<stage>.md                   objective, reader, failure modes, then the handoff document(s)
```

The issue fence sits at the **end of the base**, so the stage's own instructions come after
it and the base closes with "Your instructions for this stage follow." The model reads its
actual task after the thing it is meant to treat as data.

A stage that reads a handoff document does have untrusted text last — a handoff has to come
after the instructions saying what to do with it — so those documents carry a fence of their
own, and the base says what a fenced region is worth before any of them appear.

Handoff documents are appended at the end of each stage file under an `###` heading. They
are prior-stage output, so they are semi-trusted at best — the base's fence covers the
issue text specifically, but a plan (or a report, or a review) can carry issue text forward.
That is an accepted risk, noted below.

## Template variables

| Variable | Used in | Value |
|---|---|---|
| `{{ticket_number}}` | `base.md`, `implement.md` | GitHub issue number |
| `{{fence_nonce}}` | `base.md` (both fence tags) | per-run random token, see below |
| `{{ticket_title}}` | `base.md` (inside fence) | issue title, verbatim |
| `{{ticket_body}}` | `base.md` (inside fence) | issue body, verbatim |
| `{{ticket_comments}}` | `base.md` (inside fence) | rendered comment thread, verbatim; empty string when none |
| `{{plan}}` | `implement.md` | `document` from the `plan` stage |
| `{{previous_implement_report}}` | `implement.md` | `report` from this run's own previous `implement` turn, or a declared-absence sentence on turn one |
| `{{review_findings}}` | `implement.md` | the most recent `review` turn's findings, rendered as prose, or a declared-absence sentence before `review` has run |
| `{{implementation_report}}` | `review.md` | `report` from the most recent `implement` turn |
| `{{previous_review_findings}}` | `review.md` | this run's own previous `review` turn's findings, rendered as prose, or a declared-absence sentence on `review`'s first turn |

`plan.md` interpolates nothing of its own — everything it needs is in the base.

Note the naming rule: each variable is named for what its **producing** stage calls its own
output, or for what it is from the **reading** stage's point of view when the same document
means something different to two readers (`review_findings` vs `previous_review_findings` are
the same underlying `work.ReviewOutput.Findings`, named for whether the reader is `implement`
picking up review's work or `review` comparing against its own last turn).

### Declaring absence instead of omitting a section

Unlike the old five-stage pipeline (every document either existed or the run hadn't reached
that stage yet), `implement` and `review` each loop, so "does this turn have a previous turn to
read" varies turn to turn *within* a stage's own file — but `interpolate` is strict in both
directions (`internal/prompts/templates.go`): a template's placeholders and the values handed to
it must match exactly, so a placeholder cannot be conditionally present. `previous_implement_report`,
`review_findings` and `previous_review_findings` are therefore always rendered, and
`internal/prompts/input.go`'s `previousImplementReportProse`/`findingsProse` substitute a plain
sentence ("this is the first implement turn of this run…") when there is nothing real to show.
One consequence worth knowing: `implement`'s and `review`'s document-fence counts are now
**constant per stage** regardless of which turn is rendering — 3 for `implement` (plan,
previous report, review findings), 2 for `review` (implementation report, previous findings) —
because the fallback sentence is fenced exactly like real content would be.

### `{{fence_nonce}}` — two requirements on the worker

The fence tags are `<untrusted-ticket-text-{{fence_nonce}}>` and its closing form. Both
requirements are on the worker rendering the prompt, and the prompt cannot enforce either:

1. **Generate a fresh random nonce per run** (a short hex token is enough — the tags read
   `<untrusted-ticket-text-7f3a91>`) and interpolate the same value into both tags.
2. **Strip every occurrence of that nonce from `{{ticket_title}}`, `{{ticket_body}}` and
   `{{ticket_comments}}` before interpolating them.** Without this the nonce is pointless
   the moment it appears in a prompt an attacker can read back. With a fixed literal tag,
   an issue body containing the closing string ends the fence early and everything after it
   lands as un-fenced prose immediately before "Your instructions for this stage follow" —
   the most authoritative position in the prompt.

Both are met in `fence.go`, with two choices worth naming:

- The nonce is minted **per Render**, so a run's turns each carry a different one.
  Per-run would have been enough; per-render is strictly stronger, needs no nonce threaded
  through workflow history, and means a document handed forward cannot contain the nonce of
  the prompt it is interpolated into.
- Stripping replaces the nonce with a visible marker rather than deleting it. Deletion lets
  the text either side close up into a fresh copy of the nonce, and it hides the attempt
  from whoever reads the transcript.

`checkFence` then asserts the nonce appears the expected number of times in the finished
prompt (the issue fence, plus one pair per document the stage reads) and fails the render
otherwise, so a value interpolated without being stripped is a stage that does not start
rather than a fence that can be forged.

### `{{ticket_comments}}` — source

Populated from `TicketDetail(ctx, number)` on the `GitHub` seam (title, body and the comment
thread), with the bot's own status comment filtered out and the thread capped. The comment
thread is where a brain-dump issue's actual clarification usually lives, which is why it is
carried.

`work.Ticket`'s doc comment warns that `Title` and `Body` are attacker-controllable. **That
warning has to extend to comments**, which are the *more* attacker-reachable field: filing
an issue is one bar, commenting on someone else's is a lower one.

## What each prompt is for

- **base** — the only thing the repo cannot tell an agent: that the pipeline is
  `plan → implement → review` and loops; that every stage is a fresh process with no memory
  of the others, except `implement`'s own later turns, which resume its previous turn's codex
  conversation; that `review` is never resumed and has no stake in defending prior work; that
  its document is the entire handoff to anything that reads it; that nobody will answer a
  question; what to do when blocked; the output contract; the fence.
- **plan** — "plan the work", plus who reads it now (`implement` directly — there is no
  separate revision stage), that the issue may be a brain dump rather than a specification, a
  bias toward the smallest change that resolves it, and the two ways plans fail (too abstract
  to act on; asserting things about the code that were never checked).
- **implement** — the only writing stage, and the only one whose turns resume each other.
  Branch already checked out, worktree rule explicitly overridden there (see below). Test-first
  with the actual output pasted in, because "I wrote the test first" is a claim and the failing
  run is evidence. Deviation from the plan (or from a review finding) is expected; silent
  deviation is the failure. Answers with `title`/`body` for the pull request the workflow opens
  or updates after every push — not a diff against the last turn's title/body, but the pull
  request as it should read right now. Says in the first line if the work was not completed.
- **review** — adversarial, checks the implementation report against the issue (not against a
  plan — there is no separate plan-review step any more) and against the repository as it
  actually is, names what is sound as well as what is wrong, marks blocking versus advisory,
  and the symmetric warning: rubber-stamping work that reads well, versus manufacturing
  findings to look thorough. Answers with a `findings` array, each with a stable `id` the model
  is instructed to reuse across turns for the same underlying issue — see `work.ReviewOutput`
  and "What a finding id is" in the pipeline-rewrite spec for why this is prompt discipline
  rather than something code can enforce.

## `AGENTS.md` overrides, named where they conflict

The base orders every stage to read and follow `AGENTS.md`, so anything this environment
contradicts has to be named or the agent is left with two opposite instructions. One is:

- `implement.md` — "never edit the main checkout, always `wtp add` a worktree first". The
  sandbox is a disposable per-ticket checkout and the branch is pre-made. Named there, once,
  and scoped explicitly to that one rule.

(The old `propose.md` carried a second override — "opening a PR… is pre-approved; not in this
pipeline" — which is moot now that no stage opens a pull request at all.)

The base carries the general form of this in one clause ("where this prompt overrides
something in it, it says so at the point of conflict; nothing else in it is suspended") so
that an override at one site is not read as licence to ignore the rest.

## Deliberately left out

- **Anything `AGENTS.md` already says** — worktrees, commit cadence, style, testing tools,
  issue-label scheme, where things live. Each stage points at `AGENTS.md` once, via the base,
  and stops.

  `implement`'s "commit before you finish, but do not push" is **not** in `AGENTS.md` and is
  load-bearing. The workflow publishes the committed head through a credentialed repository
  boundary before `OpenOrUpdatePullRequest`, so model-selected commands never receive a token.
  `Fixes #N` moved from the old `propose.md` into `implement.md`'s `body` guidance so the
  resolved issue intentionally auto-closes when its PR merges. The prompt confines that
  closing reference to the canonical linked-issue section, preventing incidental prose or
  commit messages from triggering an unrelated closure.
- **Whether a later turn should read the codebase.** Never mentioned in either direction.
  `review` is told to verify against the repository as it actually is, which encourages
  reading without framing it as unusual.
- **Required headings.** Every stage's structure is prose ("useful ground to cover", "cover
  what you changed and why"), and the base states once that headings are advisory. No stage
  presents a bulleted skeleton, because a skeleton is a form and gets filled in.
- **Token or time budgets.** No stage is told to hurry. Turn budgets are a workflow concern
  (`internal/workflows`' loop), not a prompt one.
- **Any mention of the JSON envelope.** `--output-schema` enforces the envelope shape; telling
  the model about the wrapper invites it to hand-write JSON.

## Risks a human should decide on

1. **Handoff documents are fenced, and the fence is bytes rather than judgement.** `strip` and
   `checkFence` compare under ASCII case folding and remove the tag names as well as the nonce,
   so neither a case-flipped leaked nonce nor a bare `</untrusted-ticket-text-…>` written into an
   issue body reaches a model as a second tag-shaped string. Handoff documents get a fence of
   their own (`untrusted-prior-document-…`), because a planner that quotes a malicious issue
   body otherwise carries that quote into `implement` — the one stage holding a GitHub App
   token — as un-marked bytes of its prompt, and now `review`'s findings can carry the same
   risk forward into a later `implement` turn via `review_findings`. What the fence still cannot
   reach is a string that only *resembles* a tag: Unicode confusables, or whitespace broken
   into the tag name. Those never match the opening tag the model was shown, and folding them
   away would mean normalising arbitrary issue text; the base's guard paragraph is what covers
   them. Accepted cost: an issue that quotes a tag name legitimately — a ticket *about* this
   fence is the obvious case — reaches the model with that quotation replaced by
   `[fence marker removed]`. Nothing can tell a quotation from an attempt, and a visible
   marker beats a silent deletion, but it will look like a bug to whoever hits it first.
2. **`implement` is asked to paste real test output into its report.** On a large test suite
   that could be long, and the document is carried forward into the next `implement` turn's
   own prompt and into `review`'s. No truncation guidance is given, deliberately — truncation
   instructions are how "show the failing output" degrades back into "claim you ran it". If
   reports come back enormous in practice, cap it in the prompt with evidence rather than
   pre-emptively.

   Issue text *is* capped, at 20000 bytes for the description and 20000 for the whole
   comment thread, with what was cut declared in the prompt. The asymmetry is deliberate:
   the length of a handoff is chosen by a stage of this pipeline, while the length of an
   issue body is chosen by whoever filed it, and an unbounded prompt on that side is both
   an unbounded token spend and a context window an attacker can push the stage's own
   instructions out of.
3. **Finding-id sameness is exact string equality, decided by the model, checked by code.**
   `review.md` instructs the model to reuse an identical `id` for a finding it judges to be the
   same underlying issue across turns, and the workflow's stall-detection rule treats a
   surviving blocking id as terminal. A model that phrases the same defect with a new id reads
   as "new finding" and defeats the rule; a model that reuses an id for a genuinely different
   defect reads as "still broken" and ends a run that was in fact still progressing. Both are
   real, accepted risks of choosing exact-string equality over anything fuzzier — see "What a
   finding id is" in the pipeline-rewrite spec.
4. **Two things the prompts assert and cannot enforce**: "you cannot write files" in `plan`, and
   "do not create, rename or switch branches" / "do not open a pull request yourself" in
   `implement`. The first is only true if codex actually runs `plan` in a read-only sandbox
   mode — worth confirming, because as written it is either a constraint or a false statement
   about the environment. The rest are instructions and nothing more.
5. Minor: the base says nobody will answer, while the document's first line is in fact posted
   to the issue. That is deliberate — the claim is that no *reply* comes, not that nothing is
   read — but an agent could still reason its way into addressing a human in that line. Worth
   watching.
