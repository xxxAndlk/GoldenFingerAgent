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

// migrateLockKey 在进程之间串行化 schema 变更（测试套件并发执行
// Migrate；否则 CREATE TABLE 会在 pg_type 上竞争）。
const migrateLockKey = 872034721

// Migrate 按文件名顺序应用 migrations.FS 中待执行的 *.sql 文件。
// 每个文件只执行一次；已应用的文件名记录在 schema_migrations 表中。
func (d *DB) Migrate(ctx context.Context) ([]string, error) {
		// 在咨询锁下创建记账表（仅 IF NOT EXISTS 仍会在表的复合类型上竞争）。
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
			// 在同一事务内加锁并复查存在性：在锁竞争中落败的并发迁移者
			// 会看到该行并跳过。
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
