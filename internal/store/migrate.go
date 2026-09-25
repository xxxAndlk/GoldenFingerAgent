package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"goldenfinger/agent/migrations"
)

// migrateLockKey serializes schema changes across processes (test suites run
// Migrate concurrently; CREATE TABLE races on pg_type otherwise).
const migrateLockKey = 872034721

// Migrate applies pending *.sql files from migrations.FS in filename order.
// Each file runs once; applied names are tracked in schema_migrations.
func (d *DB) Migrate(ctx context.Context) ([]string, error) {
	// Create the bookkeeping table under the advisory lock (IF NOT EXISTS
	// alone still races on the table's composite type).
	if err := d.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLockKey); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
		return err
	}); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		raw, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return applied, err
		}
		// Lock + existence re-check inside one tx: a concurrent migrator that
		// lost the lock race sees the row and skips.
		done := false
		if err := d.WithTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLockKey); err != nil {
				return err
			}
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&exists); err != nil {
				return err
			}
			if exists {
				done = true
				return nil
			}
			if _, err := tx.Exec(ctx, string(raw)); err != nil {
				return fmt.Errorf("apply %s: %w", name, err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
			return err
		}); err != nil {
			return applied, err
		}
		if !done {
			applied = append(applied, name)
		}
	}
	return applied, nil
}
