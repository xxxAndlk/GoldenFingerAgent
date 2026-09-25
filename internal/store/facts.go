package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type FactRepo struct{ q Querier }

func NewFactRepo(q Querier) *FactRepo { return &FactRepo{q: q} }

const factCols = `id, person_id, fact_type, value_text, confidence, status,
	source_msg_id, valid_from, valid_to, created_at, updated_at, deleted_at`

func scanFact(row pgx.Row) (*Fact, error) {
	f := &Fact{}
	err := row.Scan(&f.ID, &f.PersonID, &f.FactType, &f.ValueText, &f.Confidence, &f.Status,
		&f.SourceMsgID, &f.ValidFrom, &f.ValidTo, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (r *FactRepo) Insert(ctx context.Context, f *Fact) error {
	var emb any
	if len(f.Embedding) > 0 {
		emb = vecToString(f.Embedding)
	}
	return r.q.QueryRow(ctx, `
		INSERT INTO fact (person_id, fact_type, value_text, confidence, status,
			source_msg_id, valid_from, valid_to, embedding)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::vector)
		RETURNING id, created_at, updated_at`,
		f.PersonID, f.FactType, f.ValueText, f.Confidence, f.Status,
		f.SourceMsgID, f.ValidFrom, f.ValidTo, emb,
	).Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt)
}

// SetValidity closes a fact's validity window (supersede keeps the row for traceability).
func (r *FactRepo) SetValidity(ctx context.Context, id string, validTo *time.Time) error {
	_, err := r.q.Exec(ctx,
		`UPDATE fact SET valid_to = $2, updated_at = now() WHERE id = $1`, id, validTo)
	return err
}

func (r *FactRepo) ListByPerson(ctx context.Context, personID string) ([]Fact, error) {
	rows, err := r.q.Query(ctx, `
		SELECT `+factCols+` FROM fact
		WHERE person_id = $1 AND deleted_at IS NULL AND valid_to IS NULL
		ORDER BY created_at DESC`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// ListCurrentByOwner returns all current (non-deleted, valid) facts for a user's persons.
func (r *FactRepo) ListCurrentByOwner(ctx context.Context, ownerID string, limit int) ([]Fact, error) {
	rows, err := r.q.Query(ctx, `
		SELECT f.id, f.person_id, f.fact_type, f.value_text, f.confidence, f.status,
			f.source_msg_id, f.valid_from, f.valid_to, f.created_at, f.updated_at, f.deleted_at
		FROM fact f JOIN person p ON p.id = f.person_id
		WHERE p.owner_user_id = $1 AND p.deleted_at IS NULL
		  AND f.deleted_at IS NULL AND f.valid_to IS NULL
		ORDER BY f.updated_at DESC LIMIT $2`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

func scanFacts(rows pgx.Rows) ([]Fact, error) {
	var out []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.PersonID, &f.FactType, &f.ValueText, &f.Confidence, &f.Status,
			&f.SourceMsgID, &f.ValidFrom, &f.ValidTo, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InferredFact is a current inferred fact with its person name (digest prompts).
type InferredFact struct {
	Fact
	PersonName string `json:"person_name"`
}

// ListInferredCurrent returns current inferred (not yet confirmed) facts —
// the daily digest asks the user to confirm these (memory consolidation).
func (r *FactRepo) ListInferredCurrent(ctx context.Context, ownerID string) ([]InferredFact, error) {
	rows, err := r.q.Query(ctx, `
		SELECT f.id, f.person_id, f.fact_type, f.value_text, f.confidence, f.status,
			f.source_msg_id, f.valid_from, f.valid_to, f.created_at, f.updated_at, f.deleted_at,
			p.canonical_name
		FROM fact f JOIN person p ON p.id = f.person_id
		WHERE p.owner_user_id = $1 AND p.deleted_at IS NULL
		  AND f.deleted_at IS NULL AND f.valid_to IS NULL AND f.status = 'inferred'
		ORDER BY f.created_at ASC
		LIMIT 20`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InferredFact
	for rows.Next() {
		var item InferredFact
		if err := rows.Scan(&item.ID, &item.PersonID, &item.FactType, &item.ValueText, &item.Confidence, &item.Status,
			&item.SourceMsgID, &item.ValidFrom, &item.ValidTo, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt,
			&item.PersonName); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// FindConflict returns current facts of the same (person, fact_type) with a different value.
func (r *FactRepo) FindConflict(ctx context.Context, personID, factType, valueText string) ([]Fact, error) {
	rows, err := r.q.Query(ctx, `
		SELECT `+factCols+` FROM fact
		WHERE person_id = $1 AND fact_type = $2 AND value_text <> $3
		  AND deleted_at IS NULL AND valid_to IS NULL`, personID, factType, valueText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// Similar ranks facts by pgvector cosine distance against the query embedding.
func (r *FactRepo) Similar(ctx context.Context, ownerID string, vec []float32, k int) ([]ScoredFact, error) {
	rows, err := r.q.Query(ctx, `
		SELECT f.id, f.person_id, f.fact_type, f.value_text, f.confidence, f.status,
			f.source_msg_id, f.valid_from, f.valid_to, f.created_at, f.updated_at, f.deleted_at,
			1 - (f.embedding <=> $2::vector) AS score
		FROM fact f JOIN person p ON p.id = f.person_id
		WHERE p.owner_user_id = $1 AND p.deleted_at IS NULL
		  AND f.deleted_at IS NULL AND f.embedding IS NOT NULL
		ORDER BY f.embedding <=> $2::vector
		LIMIT $3`, ownerID, vecToString(vec), k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScoredFact
	for rows.Next() {
		var f Fact
		var score float64
		if err := rows.Scan(&f.ID, &f.PersonID, &f.FactType, &f.ValueText, &f.Confidence, &f.Status,
			&f.SourceMsgID, &f.ValidFrom, &f.ValidTo, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt, &score); err != nil {
			return nil, err
		}
		out = append(out, ScoredFact{Fact: f, Score: score})
	}
	return out, rows.Err()
}

// HardDelete removes a fact outright (forget cascade).
func (r *FactRepo) HardDelete(ctx context.Context, id string) error {
	_, err := r.q.Exec(ctx, `DELETE FROM fact WHERE id = $1`, id)
	return err
}

// Get returns one fact regardless of owner (caller checks ownership).
func (r *FactRepo) Get(ctx context.Context, id string) (*Fact, error) {
	return scanFact(r.q.QueryRow(ctx, `SELECT `+factCols+` FROM fact WHERE id = $1`, id))
}
