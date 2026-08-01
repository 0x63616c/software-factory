# Software Factory

Software Factory turns dependency-ready Tickets into reviewed, tested, exact-head
squash merges. Its durable Temporal workflows own planning, implementation,
independent review, bounded revision, and lifecycle recovery. Model calls use the
Responses interface directly; the retired Codex CLI prototype is not part of the
active runtime.

The previously tracked standalone prototype is preserved byte-for-byte under
[`_archived/`](./_archived/) for later review. It is intentionally excluded from
active build, lint, generation, test, image, and CI surfaces.

## Current migration state

The activated production implementation was imported from
`world-wide-webb/apps/software-factory`. Standalone contributor, integration,
end-to-end, and release contracts are being established before the production
consumer is cut over.

## Architecture

```text
dependency-ready Ticket
  -> Dispatcher
  -> WorkOnTicket
  -> AgentWorkflow(plan)
  -> AgentWorkflow(implement)
  -> draft PR + required CI
  -> AgentWorkflow(review)
  -> bounded revision when needed
  -> exact-reviewed-head squash merge
  -> Run succeeded + Ticket done
```

See [`docs/system-map.md`](./docs/system-map.md) for the detailed trust and
execution map.

## Contributor commands

```bash
just bootstrap
just archive-check
just verify
just integration
```

`just e2e` and `just release-check VERSION=v0.1.0` are reserved by the v1
acceptance contract and will be enabled as their implementation slices land.
