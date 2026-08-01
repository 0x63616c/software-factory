-- name: MigrationProbeExists :one
SELECT EXISTS (
    SELECT FROM pg_catalog.pg_tables
    WHERE schemaname = 'public' AND tablename = 'migration_probe'
) AS exists;
