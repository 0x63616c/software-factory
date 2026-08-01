# Software Factory

Software Factory turns dependency-ready Tickets into reviewed, tested, exact-head
squash merges. Its durable Temporal workflows own planning, implementation,
independent review, bounded revision, and lifecycle recovery. Model calls use the
Responses interface directly; the retired Codex CLI prototype is not part of the
active runtime.

[![CI](https://github.com/0x63616c/software-factory/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/0x63616c/software-factory/actions/workflows/ci.yml)
[![Last commit](https://img.shields.io/github/last-commit/0x63616c/software-factory?style=flat-square&label=Last%20Commit&labelColor=1f2937&color=0ea5e9&logo=github&logoColor=white)](https://github.com/0x63616c/software-factory/commits/main)

The previously tracked standalone prototype is preserved byte-for-byte under
[`_archived/`](./_archived/) for later review. It is intentionally excluded from
active build, lint, generation, test, image, and CI surfaces.

## Extraction state

The activated production implementation was imported from
`world-wide-webb/apps/software-factory`. Standalone contributor, integration,
end-to-end, image, and release contracts are active. The production consumer
remains on the embedded build until the first immutable release is published
and its exact digests pass the production cutover gate.

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
execution map and [`docs/configuration.md`](./docs/configuration.md) for the
operator-owned dependencies, environment contract, and secret boundaries.

## Contributor commands

```bash
just bootstrap
just archive-check
just verify
just integration
just e2e
```

`just e2e` starts disposable PostgreSQL and Temporal services, drives a Ticket
through the real durable `Dispatcher -> WorkOnTicket -> AgentWorkflow` path,
and writes its machine-checkable result to `.artifacts/e2e/result.json`. It
requires Docker and `jq`; only the model and GitHub boundaries are faked.

## Releases

Stable SemVer tags publish seven immutable `linux/amd64` images plus a digest
manifest and checksums. Consumers pin digests; the project does not publish
moving `latest`, major, or minor aliases. See [releasing](./docs/releasing.md)
and the [compatibility policy](./docs/compatibility.md).

Contributions follow [`CONTRIBUTING.md`](./CONTRIBUTING.md). Report security
issues privately as described in [`SECURITY.md`](./SECURITY.md).
