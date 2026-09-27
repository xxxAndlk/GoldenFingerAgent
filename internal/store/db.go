// Package store 是 SQLite 持久化层（相当于 pi 的 session-backends）。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// 注册 "sqlite" 驱动（纯 Go，CGO_ENABLED=0）。
	_ "modernc.org/sqlite"
)

// Querier 是各仓储使用的 database/sql 子集。*sql.DB 与 *sql.Tx 都满足它。
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB 封装 SQLite 连接，并带一个轻量事务辅助方法。
type DB struct {
	*sql.DB
}

// Connect 打开（必要时创建）SQLite 数据库文件。DSN 附带 busy_timeout/WAL/
// foreign_keys pragma；单连接模式彻底消除 SQLITE_BUSY 写锁竞争。
func Connect(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	path = filepath.ToSlash(path)
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("connect db: %w", err)
	}
	// 单连接：家庭单进程负载下避免写锁竞争，也保证 :memory: 库不丢。
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &DB{DB: db}, nil
}

func (d *DB) Close() { d.DB.Close() }

// WithTx 在事务内运行 fn，成功时提交。
func (d *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // 提交之后的回滚是空操作
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// nowForDB 返回写入时间列的时间（显式传入，不依赖 DB 默认值）。
func nowForDB() time.Time { return time.Now() }

// dsnPath 供测试/工具使用：返回不含 pragma 的裸路径。
func dsnPath(dsn string) string { return strings.Split(dsn, "?")[0] }
