---
name: add-migration
description: Use when changing the database schema — adds a goose SQL migration that the binary embeds and auto-runs on startup, and regenerates sqlc.
---

# Adding a database migration

We use **goose** (plain SQL, embedded via `embed.FS`, auto-run on startup, fail-fast)
and **sqlc** (typed Go generated from the schema + queries). Migrations are the schema
source of truth; sqlc reads them. Driver is `modernc.org/sqlite` (pure Go, no cgo).
See ADR: persistence.

## Steps

1. **Create the migration** under the migrations dir (embedded with `//go:embed`),
   named `NNNN_short_description.sql`, next number in sequence. Use goose annotations:
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
   Always write the `Down` — but note migrating *down* in anger is a stop-and-ask action.

2. **Add/adjust queries** in the sqlc query files (`.sql` with `-- name: X :one|:many|:exec`).

3. **Regenerate:** `sqlc generate`. The generated types stay **sealed in
   `internal/store`** — they must not leak out (depguard enforces `database/sql` is
   banned elsewhere; keep the generated package internal too). `emit_interface: true`
   gives you the `Querier` interface for free.

4. **Do not hand-write a repository wrapper.** Domain code depends on the store's
   narrow interface; the generated interface is mocked *only* to force failures
   (disk-full/constraint/timeout). Success-path tests run against real `:memory:`.

5. **Verify:** the binary auto-migrates on startup and fails fast with a helpful error
   if a migration fails. Run the store's integration tests (real sqlite file) and the
   unit tests (`:memory:`).

## Do not
- Reach for `mattn/go-sqlite3` (cgo). We are pure-Go on purpose.
- Let `sqlc`/`sql` types escape `internal/store` (tenet #4, no leaky abstractions).
