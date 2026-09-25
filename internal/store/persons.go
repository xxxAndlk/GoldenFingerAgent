package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type PersonRepo struct{ q Querier }

func NewPersonRepo(q Querier) *PersonRepo { return &PersonRepo{q: q} }

func (r *PersonRepo) Create(ctx context.Context, p *Person) error {
	return r.q.QueryRow(ctx, `
		INSERT INTO person (owner_user_id, canonical_name, notes)
		VALUES ($1, $2, $3)
		RETURNING id, created_at`,
		p.OwnerUserID, p.CanonicalName, p.Notes,
	).Scan(&p.ID, &p.CreatedAt)
}

func (r *PersonRepo) Get(ctx context.Context, ownerID, id string) (*Person, error) {
	p := &Person{}
	err := r.q.QueryRow(ctx, `
		SELECT id, owner_user_id, canonical_name, notes, created_at, deleted_at
		FROM person WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL`, id, ownerID).
		Scan(&p.ID, &p.OwnerUserID, &p.CanonicalName, &p.Notes, &p.CreatedAt, &p.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *PersonRepo) ListByOwner(ctx context.Context, ownerID string) ([]Person, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, owner_user_id, canonical_name, notes, created_at, deleted_at
		FROM person WHERE owner_user_id = $1 AND deleted_at IS NULL
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

// FindByAlias returns persons whose canonical name or any alias matches (case-insensitive).
func (r *PersonRepo) FindByAlias(ctx context.Context, ownerID, name string) ([]Person, error) {
	rows, err := r.q.Query(ctx, `
		SELECT DISTINCT p.id, p.owner_user_id, p.canonical_name, p.notes, p.created_at, p.deleted_at
		FROM person p
		LEFT JOIN alias a ON a.person_id = p.id
		WHERE p.owner_user_id = $1 AND p.deleted_at IS NULL
		  AND (lower(p.canonical_name) = lower($2) OR lower(a.alias) = lower($2))
		ORDER BY p.created_at`, ownerID, name)
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
	_, err := r.q.Exec(ctx, `
		INSERT INTO alias (person_id, alias) VALUES ($1, $2)
		ON CONFLICT (person_id, alias) DO NOTHING`, personID, alias)
	return err
}

func (r *PersonRepo) ListAliases(ctx context.Context, personID string) ([]Alias, error) {
	rows, err := r.q.Query(ctx,
		`SELECT id, person_id, alias FROM alias WHERE person_id = $1`, personID)
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

// SoftDeleteCascade forgets a person: soft-deletes the person row, hard-deletes
// aliases and facts (including vectors — "删除后不可检索").
func (r *PersonRepo) SoftDeleteCascade(ctx context.Context, ownerID, personID string) error {
	return withTx(ctx, r.q, func(q Querier) error {
		tag, err := q.Exec(ctx, `
			UPDATE person SET deleted_at = now()
			WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL`, personID, ownerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := q.Exec(ctx, `DELETE FROM alias WHERE person_id = $1`, personID); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `DELETE FROM fact WHERE person_id = $1`, personID)
		return err
	})
}

// UpdateNotes edits the person page notes.
func (r *PersonRepo) UpdateNotes(ctx context.Context, ownerID, personID, notes string) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE person SET notes = $3
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL`, personID, ownerID, notes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// withTx runs fn in a transaction when q is not already a tx-capable runner.
// The pool is used when q is *pgxpool.Pool; if q is already a tx it reuses it.
func withTx(ctx context.Context, q Querier, fn func(Querier) error) error {
	type txBeginner interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	}
	if tb, ok := q.(txBeginner); ok {
		tx, err := tb.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return fn(q)
}
