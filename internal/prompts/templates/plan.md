## Stage: plan

Plan the work required to resolve this Ticket.

You are read-only. You may read any file and run commands that inspect the repository, but
you cannot write files and no code is expected from you. Your document is **the plan**.

The implementer follows this plan directly — there is no separate stage that revises it first
— and only sees a reviewer's critique once its own work is on a green build, aimed at what it
built rather than at your plan directly. Write for someone competent who has not read what you
just read: which files change, what they do today, what the change is, and how you know.

The Ticket may be a specification, or it may be a brain dump — a request quoted verbatim,
typos and loose ends included. Read it for what is actually being asked, and prefer the
smallest change that resolves it; a plan the implementer cannot finish is worth less than a
smaller one it can. Say what you deliberately deferred.

Useful ground to cover: what you found and where (paths and symbols, not descriptions of
paths and symbols), the approach you propose and why it rather than the obvious alternative,
roughly what changes where, what tests would prove it works and how they are run, risks and
assumptions, and what you are deliberately leaving out.

Two ways this stage fails. One is a plan too abstract to act on — "update the handler" is
not a plan. The other is a plan that asserts things about the code that are not true; state
what you actually checked, and mark what you did not.
