---
name: add-migration
description: Use when changing the database schema — adds a goose SQL migration that the binary embeds and auto-runs on startup, and regenerates sqlc.
---

# Adding a database migration

**goose** (plain SQL, embedded via `embed.FS`, auto-run on startup, fail-fast) + **sqlc**
(typed Go from schema + queries). Migrations are the schema source of truth; sqlc reads
them. Driver: `modernc.org/sqlite` (pure Go, no cgo). See [ADR-0010].

## Steps

1. **New migration** under the migrations dir (`//go:embed`), named
   `NNNN_short_description.sql`, next in sequence:
   ```sql
   -- +goose Up
   -- +goose StatementBegin
   CREATE TABLE ... ;
   -- +goose StatementEnd

   -- +goose Down
   -- +goose StatementBegin
   DROP TABLE ... ;
   -- +goose StatementEnd
   ```
   Always write `Down` — but migrating *down* in anger is a stop-and-ask action.
2. **Add/adjust queries** in the sqlc files (`-- name: X :one|:many|:exec`).
3. **`sqlc generate`.** Generated types stay **sealed in `store`** (depguard bans
   `database/sql` elsewhere). `emit_interface: true` gives the `Querier` interface free.
4. **No hand-written repository wrapper.** Domain code depends on the store's narrow
   interface; mock the generated interface *only* to force failures (disk-full,
   constraint, timeout). Success paths run against real `:memory:`.
5. **Verify:** binary auto-migrates on startup, fails fast with a helpful error. Run the
   store's integration tests (real sqlite file) + unit tests (`:memory:`).

## Do not
- Reach for `mattn/go-sqlite3` (cgo). Pure-Go on purpose.
- Let `sqlc`/`sql` types escape `store` (tenet 4, no leaky abstractions).
