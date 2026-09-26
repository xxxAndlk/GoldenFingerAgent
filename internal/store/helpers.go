package store

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errNoRows = pgx.ErrNoRows

// notNullJSON 为 jsonb 列返回 JSON 安全的值。
func notNullJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

// isUniqueViolation 判断 err 是否为 Postgres 唯一约束冲突。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
