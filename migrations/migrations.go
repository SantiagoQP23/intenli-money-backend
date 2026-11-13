// Package migrations embeds the SQL migrations and applies them with goose.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed *.sql
var files embed.FS

// Up applies all pending migrations.
func Up(ctx context.Context, databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	defer db.Close()

	// The session lock lets several API instances start at once safely.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("init migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files,
		goose.WithSessionLocker(locker),
		goose.WithGoMigrations(goose.NewGoMigration(6,
			// Runs once: later changes to the defaults only affect new workspaces.
			&goose.GoFunc{RunTx: BackfillDefaultCategories},
			&goose.GoFunc{Mode: goose.TransactionEnabled}, // nothing to undo: rows may be in use
		)))
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
