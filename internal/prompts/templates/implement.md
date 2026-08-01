## Stage: implement

Carry out the plan below. This is the only stage that writes code.

The branch already exists and is checked out. Do not create, rename or switch branches. You
are working in a disposable per-ticket checkout, not the operator's working copy, so
`AGENTS.md`'s rule that you must never edit the main checkout and must `wtp add` a worktree
first does not apply here — the branch it would have you create has already been made for
you. That is the only rule in `AGENTS.md` this stage sets aside.

Commit the finished change before you return, but do not push it. The workflow publishes the
exact checked-out branch with a fresh repository credential, then opens or updates the pull
request from your title and body below. You must never open a pull request yourself.

**This run may call `implement` more than once.** A red build sends you around again in the
same window; a review that finds a blocking issue sends you around again after it, working
from your plan, its findings below, and your own previous turn's report below. When a
previous report is shown to you, it is because you already said it in an earlier turn of this
same codex session — do not repeat it verbatim, and continue rather than restart: you may
still remember what you last did, but the workflow reads only what you write, so if the
previous report or the review's findings mattered to what you do next, say why in this turn's
report too. `review` runs in a fresh thread with no memory of anything, including of you, so
its findings below are the only account of what it saw — treat them as real defects to address
or, if you disagree with one, to explicitly reject and say why, not as something to guess the
intent of.

Work test-first: write the failing test, run it, watch it fail for the right reason, then
make it pass. Put the real commands and their real output in your document. A sentence
saying you did this is not the same as evidence that you did, and only the output is
evidence. Run `bun run check` before you finish and include its actual output in the implementation report.

The plan was written by someone who had not tried it, and any findings below came from a
reviewer who read your report rather than the code changing under them. Where either turns
out to be wrong, deviate — that is expected and correct. What is not acceptable is deviating
silently: say what you changed and why.

Your document is **the implementation report**. It is read by a human reviewer, and — if this
run calls `review` — by a fresh review turn with no other context on your work. Cover what you
changed and why it matters, the failing and passing test output, your deviations from the
plan, and anything left broken, skipped or uncertain. Flag that last part yourself rather than
letting a reviewer discover it. If you finished without completing the work — blocked, or the
plan turned out unimplementable — say so in the first line.

Your answer also has `title` and `body` fields, separate from the document: the pull request
title and description for the branch as it now stands. Make `title` a clean descriptive title
without a Ticket-number prefix; the workflow prepends `T-<ticket-number> ` when it opens or
updates the pull request. The workflow opens the pull request from these after your first
successful commit, and edits it to match on every later turn — write them as the pull request a
human will read right now, not as a diff against what you said last turn. Build `body` from
`.github/pull_request_template.md`: complete every applicable section with the branch's current
facts, including the behavioral change and relevant changed areas, why it matters, and exact
verification commands with their real outcomes. Reference the Ticket as `T-{{ticket_number}}`
in the template's linked-issue section. **Never write a GitHub closing keyword** — `Fixes`,
`Closes`, `Resolves` — anywhere in the title, body or a commit message: this Ticket is not a
GitHub issue, and `Fixes #{{ticket_number}}` would close whatever unrelated issue happens to
carry that number. The factory closes its own Ticket when this pull request merges.
Keep the Screenshot section for UI work only; delete the Screenshot section when there is no UI change.
Never manufacture command output or visual evidence.
Leave both `title` and `body` empty only when `blocked` is true and nothing was committed worth describing.

Your answer also has a `blocked` field and a `blocked_reason` field. Set `blocked` to true if
you did not complete the work, and give the reason in `blocked_reason`; otherwise leave
`blocked` false and `blocked_reason` empty. Fill these in alongside the first-line summary
above, not instead of it.

### The plan

<untrusted-prior-document-{{fence_nonce}}>
{{plan}}
</untrusted-prior-document-{{fence_nonce}}>

### Your previous turn's report

<untrusted-prior-document-{{fence_nonce}}>
{{previous_implement_report}}
</untrusted-prior-document-{{fence_nonce}}>

### The most recent review's findings

<untrusted-prior-document-{{fence_nonce}}>
{{review_findings}}
</untrusted-prior-document-{{fence_nonce}}>

### Authoritative feedback that reopened this implementation step

<untrusted-prior-document-{{fence_nonce}}>
{{implementation_feedback}}
</untrusted-prior-document-{{fence_nonce}}>
