# ADR-0015: Agent operating protocol

- Status: Accepted
- Date: 2026-07-23

## Context
Distinct from *what the code looks like* (the other ADRs), we need to fix *how an agent
behaves while working on this repo* — its dev loop, when it tests, commits, and when it
must stop and ask. This is an autonomous, code-shipping system, so the brakes matter.

## Decision
- **Branch per task; never commit to `main`.** PR to merge. Conventional, atomic commits.
- **TDD is mandatory test-first for engine/domain code** (red-green-refactor). It is the
  procedural twin of the testability floor (ADR-0002) and it *forces* the narrow-door
  design — you can't write an untestable interface if the test comes first.
- **For UI code, TDD is preferred but left to judgment** (user/LLM/skill) — never
  blocked, never forced. superpowers may TDD the UI freely.
- **Done-loop, hook-enforced:** a change is not "done" until `golangci-lint` and the
  relevant tests pass (harness `Stop`/`PostToolUse` hook — enforcement pyramid rung 3).
- **Verification before completion:** never claim "fixed"/"passes" without the command
  output that proves it. Evidence before assertion.
- **Stop and ask** before anything irreversible or outward-facing: force-push, merge to
  `main`, deleting data / migrating down in anger, side-effecting external calls.

## Rejected alternatives
- **TDD encouraged-not-enforced**: rejected for engine/domain — that's the "hope they
  read the skill" failure mode (ADR-0013).
- **Mandatory TDD everywhere including UI**: rejected — UI is genuinely harder to justify
  test-first; leave it to judgment rather than force low-value tests.
- **Global coverage gate / test-required-but-not-first**: rejected (see ADR-0012); loses
  the design pressure test-first gives.

## Consequences
The protocol is encoded in `AGENTS.md` (always-loaded) and backed by hooks, not left to
a skill. It is the human-scale governance layer over an autonomous factory.
