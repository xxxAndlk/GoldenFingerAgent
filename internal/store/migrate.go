package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"goldenfinger/agent/migrations"
)

// migrateMu 在进程之间串行化 schema 变更（测试套件并发执行 Migrate）。
var migrateMu sync.Mutex

// Migrate 按文件名顺序应用 migrations.FS 中待执行的 *.sql 文件。
// 每个文件只执行一次；已应用的文件名记录在 schema_migrations 表中。
// SQLite 的 Exec 不保证执行多语句，因此按 ';' 裸拆逐条执行（本批 DDL
// 无函数/触发器/含分号字面量，可安全拆分），整文件包在一个事务内。
func (d *DB) Migrate(ctx context.Context) ([]string, error) {
	migrateMu.Lock()
	defer migrateMu.Unlock()

	// 先建记账表（DDL 可事务，统一在后续 WithTx 中完成亦可；此处单独建
	// 保证多连接并发下记账表一定存在）。
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL
	)`); err != nil {
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
		// 事务内复查存在性并应用：并发迁移者在锁竞争中落败会看到该行并跳过。
		done := false
		if err := d.WithTx(ctx, func(tx *sql.Tx) error {
			var exists bool
			if err := tx.QueryRowContext(ctx,
				`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = ?)`, name).Scan(&exists); err != nil {
				return err
			}
			if exists {
				done = true
				return nil
			}
			for _, stmt := range splitStatements(string(raw)) {
				if strings.TrimSpace(stmt) == "" {
					continue
				}
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("apply %s: %w", name, err)
				}
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now())
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

// splitStatements 按分号拆分 SQL 脚本（去除行注释）。
func splitStatements(raw string) []string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		lines = append(lines, line)
	}
	return strings.Split(strings.Join(lines, "\n"), ";")
}
