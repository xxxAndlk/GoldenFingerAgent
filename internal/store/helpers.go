package store

import (
	"database/sql"
	"strings"
)

var errNoRows = sql.ErrNoRows

// notNullJSON 为 JSON 文本列返回 JSON 安全的值。
func notNullJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

// isUniqueViolation 判断 err 是否为 SQLite 唯一约束冲突
// （SQLITE_CONSTRAINT_UNIQUE / SQLITE_CONSTRAINT_PRIMARYKEY）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY constraint failed")
}
