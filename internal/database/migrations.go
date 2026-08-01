// Package database applies the software factory's embedded PostgreSQL migrations.
package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrations contains the SQL migrations shipped with the API binary.
//
//go:embed migrations/*.sql
var migrations embed.FS

// ApplyMigrations applies every pending embedded PostgreSQL migration.
func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	migrationFiles, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationFiles)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	return nil
}
