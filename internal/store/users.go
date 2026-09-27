package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrNotFound 在行不存在（或被软删除）时返回。
var ErrNotFound = errors.New("store: not found")

type UserRepo struct{ q Querier }

func NewUserRepo(q Querier) *UserRepo { return &UserRepo{q: q} }

func (r *UserRepo) Create(ctx context.Context, u *User) error {
	u.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `
		INSERT INTO app_user (id, user_type, name, tz, guardian_id, notif_prefs_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at`,
		u.ID, u.UserType, u.Name, u.TZ, u.GuardianID, notNullJSON(u.NotifPrefs), nowForDB(),
	).Scan(&u.CreatedAt)
}

func (r *UserRepo) Get(ctx context.Context, id string) (*User, error) {
	u := &User{}
	var prefs []byte
	err := r.q.QueryRowContext(ctx, `
		SELECT id, user_type, name, tz, guardian_id, notif_prefs_json, created_at, deleted_at
		FROM app_user WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&u.ID, &u.UserType, &u.Name, &u.TZ, &u.GuardianID, &prefs, &u.CreatedAt, &u.DeletedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	u.NotifPrefs = prefs
	return u, nil
}

func (r *UserRepo) UpdateNotifPrefs(ctx context.Context, id string, prefs []byte) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE app_user SET notif_prefs_json = ? WHERE id = ? AND deleted_at IS NULL`,
		notNullJSON(prefs), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetUserType 是合规检查使用的廉价查询。
func (r *UserRepo) GetUserType(ctx context.Context, id string) (string, error) {
	var t string
	err := r.q.QueryRowContext(ctx,
		`SELECT user_type FROM app_user WHERE id = ? AND deleted_at IS NULL`, id).Scan(&t)
	if err != nil {
		return "", mapNotFound(err)
	}
	return t, nil
}

// ListAll 返回所有未删除用户（调度器每日摘要扫描）。
func (r *UserRepo) ListAll(ctx context.Context) ([]User, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, user_type, name, tz, guardian_id, notif_prefs_json, created_at, deleted_at
		FROM app_user WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var prefs []byte
		if err := rows.Scan(&u.ID, &u.UserType, &u.Name, &u.TZ, &u.GuardianID, &prefs, &u.CreatedAt, &u.DeletedAt); err != nil {
			return nil, err
		}
		u.NotifPrefs = prefs
		out = append(out, u)
	}
	return out, rows.Err()
}

func mapNotFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("store: %w", err)
}
