# Contributing

Software Factory is a Go service with a React console. Keep changes small,
auditable, and grounded in the durable workflow contract described in
[`docs/system-map.md`](./docs/system-map.md).

## Before opening a pull request

1. Use Go 1.26.5, Bun 1.2.19, sqlc 1.31.1, golangci-lint 2.12.2, Docker, and
   `just`. `just bootstrap` prints the installed versions.
2. Read [`AGENTS.md`](./AGENTS.md) and
   [`docs/SoftwareStyle.md`](./docs/SoftwareStyle.md).
3. Run `just verify`, `just integration`, and `just e2e`.
4. Update documentation when behavior, configuration, generated contracts, or
   operator responsibilities change.

Generated SQL, OpenAPI, and TypeScript client output must be regenerated through
`scripts/regenerate.sh`; do not hand-edit generated files. `_archived/` is
historical evidence and is excluded from every active development surface.

Pull requests should explain the user-visible outcome, the durable workflow or
trust boundary affected, and the exact verification performed. A change must
not reintroduce the retired Codex CLI execution path: model work belongs in the
durable `AgentWorkflow` and direct Responses adapter.
