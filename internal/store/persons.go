package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

type PersonRepo struct{ q Querier }

func NewPersonRepo(q Querier) *PersonRepo { return &PersonRepo{q: q} }

func (r *PersonRepo) Create(ctx context.Context, p *Person) error {
	p.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `
		INSERT INTO person (id, owner_user_id, canonical_name, notes, created_at)
		VALUES (?, ?, ?, ?, ?)
		RETURNING created_at`,
		p.ID, p.OwnerUserID, p.CanonicalName, p.Notes, nowForDB(),
	).Scan(&p.CreatedAt)
}

func (r *PersonRepo) Get(ctx context.Context, ownerID, id string) (*Person, error) {
	p := &Person{}
	err := r.q.QueryRowContext(ctx, `
		SELECT id, owner_user_id, canonical_name, notes, created_at, deleted_at
		FROM person WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL`, id, ownerID).
		Scan(&p.ID, &p.OwnerUserID, &p.CanonicalName, &p.Notes, &p.CreatedAt, &p.DeletedAt)
	if err != nil {
		if errors.Is(err, errNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *PersonRepo) ListByOwner(ctx context.Context, ownerID string) ([]Person, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, owner_user_id, canonical_name, notes, created_at, deleted_at
		FROM person WHERE owner_user_id = ? AND deleted_at IS NULL
		ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.OwnerUserID, &p.CanonicalName, &p.Notes, &p.CreatedAt, &p.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindByAlias 返回规范名或任意别名匹配的人物（大小写不敏感）。
func (r *PersonRepo) FindByAlias(ctx context.Context, ownerID, name string) ([]Person, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT DISTINCT p.id, p.owner_user_id, p.canonical_name, p.notes, p.created_at, p.deleted_at
		FROM person p
		LEFT JOIN alias a ON a.person_id = p.id
		WHERE p.owner_user_id = ? AND p.deleted_at IS NULL
		  AND (lower(p.canonical_name) = lower(?) OR lower(a.alias) = lower(?))
		ORDER BY p.created_at`, ownerID, name, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.OwnerUserID, &p.CanonicalName, &p.Notes, &p.CreatedAt, &p.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PersonRepo) AddAlias(ctx context.Context, personID, alias string) error {
	a := &Alias{ID: uuid.NewString(), PersonID: personID, Alias: alias}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO alias (id, person_id, alias) VALUES (?, ?, ?)
		ON CONFLICT (person_id, alias) DO NOTHING`, a.ID, a.PersonID, a.Alias)
	return err
}

func (r *PersonRepo) ListAliases(ctx context.Context, personID string) ([]Alias, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT id, person_id, alias FROM alias WHERE person_id = ?`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.ID, &a.PersonID, &a.Alias); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SoftDeleteCascade 遗忘一个人物：软删除人物行，硬删除别名与事实
// （含向量——“删除后不可检索”）。
func (r *PersonRepo) SoftDeleteCascade(ctx context.Context, ownerID, personID string) error {
	return withTx(ctx, r.q, func(q Querier) error {
		res, err := q.ExecContext(ctx, `
			UPDATE person SET deleted_at = ?
			WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL`, nowForDB(), personID, ownerID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM alias WHERE person_id = ?`, personID); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `DELETE FROM fact WHERE person_id = ?`, personID)
		return err
	})
}

// UpdateNotes 编辑人物页的备注。
func (r *PersonRepo) UpdateNotes(ctx context.Context, ownerID, personID, notes string) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE person SET notes = ?
		WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL`, notes, personID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// withTx 在 q 尚不具备事务能力时于事务内运行 fn。
// q 为 *sql.DB 时开启事务；若 q 已是事务则直接复用。
func withTx(ctx context.Context, q Querier, fn func(Querier) error) error {
	type txBeginner interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	}
	if tb, ok := q.(txBeginner); ok {
		tx, err := tb.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	}
	return fn(q)
}
