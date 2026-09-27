package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
)

type FactRepo struct{ q Querier }

func NewFactRepo(q Querier) *FactRepo { return &FactRepo{q: q} }

const factCols = `id, person_id, fact_type, value_text, confidence, status,
	source_msg_id, valid_from, valid_to, created_at, updated_at, deleted_at`

// Row 是 *sql.Row 与 *sql.Rows 的公共扫描接口。
type Row interface{ Scan(dest ...any) error }

func scanFact(row Row) (*Fact, error) {
	f := &Fact{}
	err := row.Scan(&f.ID, &f.PersonID, &f.FactType, &f.ValueText, &f.Confidence, &f.Status,
		&f.SourceMsgID, &f.ValidFrom, &f.ValidTo, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (r *FactRepo) Insert(ctx context.Context, f *Fact) error {
	f.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `
		INSERT INTO fact (id, person_id, fact_type, value_text, confidence, status,
			source_msg_id, valid_from, valid_to, embedding, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at, updated_at`,
		f.ID, f.PersonID, f.FactType, f.ValueText, f.Confidence, f.Status,
		f.SourceMsgID, f.ValidFrom, f.ValidTo, vecToBlob(f.Embedding), nowForDB(), nowForDB(),
	).Scan(&f.CreatedAt, &f.UpdatedAt)
}

// SetValidity 关闭事实的有效期窗口（supersede 保留该行用于溯源）。
func (r *FactRepo) SetValidity(ctx context.Context, id string, validTo *time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE fact SET valid_to = ?, updated_at = ? WHERE id = ?`, validTo, nowForDB(), id)
	return err
}

func (r *FactRepo) ListByPerson(ctx context.Context, personID string) ([]Fact, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+factCols+` FROM fact
		WHERE person_id = ? AND deleted_at IS NULL AND valid_to IS NULL
		ORDER BY created_at DESC`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// ListCurrentByOwner 返回用户全部人物当前（未删除、有效）的事实。
func (r *FactRepo) ListCurrentByOwner(ctx context.Context, ownerID string, limit int) ([]Fact, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT f.id, f.person_id, f.fact_type, f.value_text, f.confidence, f.status,
			f.source_msg_id, f.valid_from, f.valid_to, f.created_at, f.updated_at, f.deleted_at
		FROM fact f JOIN person p ON p.id = f.person_id
		WHERE p.owner_user_id = ? AND p.deleted_at IS NULL
		  AND f.deleted_at IS NULL AND f.valid_to IS NULL
		ORDER BY f.updated_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

func scanFacts(rows *sql.Rows) ([]Fact, error) {
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

// InferredFact 是附带人物名的当前推断事实（用于摘要提示）。
type InferredFact struct {
	Fact
	PersonName string `json:"person_name"`
}

// ListInferredCurrent 返回当前推断（尚未确认）的事实——
// 每日摘要会请用户确认这些事实（记忆整合）。
func (r *FactRepo) ListInferredCurrent(ctx context.Context, ownerID string) ([]InferredFact, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT f.id, f.person_id, f.fact_type, f.value_text, f.confidence, f.status,
			f.source_msg_id, f.valid_from, f.valid_to, f.created_at, f.updated_at, f.deleted_at,
			p.canonical_name
		FROM fact f JOIN person p ON p.id = f.person_id
		WHERE p.owner_user_id = ? AND p.deleted_at IS NULL
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

// FindConflict 返回同 (person, fact_type) 但取值不同的当前事实。
func (r *FactRepo) FindConflict(ctx context.Context, personID, factType, valueText string) ([]Fact, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+factCols+` FROM fact
		WHERE person_id = ? AND fact_type = ? AND value_text <> ?
		  AND deleted_at IS NULL AND valid_to IS NULL`, personID, factType, valueText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// Similar 取出候选（含 embedding BLOB），在 Go 内算余弦并排序取前 k。
// 空查询向量或全部候选无有效向量时返回空结果（与既有行为一致）。
func (r *FactRepo) Similar(ctx context.Context, ownerID string, vec []float32, k int) ([]ScoredFact, error) {
	if len(vec) == 0 {
		return nil, nil
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT f.id, f.person_id, f.fact_type, f.value_text, f.confidence, f.status,
			f.source_msg_id, f.valid_from, f.valid_to, f.created_at, f.updated_at, f.deleted_at,
			f.embedding
		FROM fact f JOIN person p ON p.id = f.person_id
		WHERE p.owner_user_id = ? AND p.deleted_at IS NULL
		  AND f.deleted_at IS NULL AND f.embedding IS NOT NULL`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScoredFact
	for rows.Next() {
		var f Fact
		var emb []byte
		if err := rows.Scan(&f.ID, &f.PersonID, &f.FactType, &f.ValueText, &f.Confidence, &f.Status,
			&f.SourceMsgID, &f.ValidFrom, &f.ValidTo, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt, &emb); err != nil {
			return nil, err
		}
		got, err := parseBlob(emb)
		if err != nil || len(got) != len(vec) {
			continue // 维度不符/损坏的向量跳过
		}
		out = append(out, ScoredFact{Fact: f, Score: float64(cosine(vec, got))})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// HardDelete 直接删除一条事实（遗忘级联）。
func (r *FactRepo) HardDelete(ctx context.Context, id string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM fact WHERE id = ?`, id)
	return err
}

// Get 返回一条事实，不校验 owner（由调用方检查归属）。
func (r *FactRepo) Get(ctx context.Context, id string) (*Fact, error) {
	return scanFact(r.q.QueryRowContext(ctx, `SELECT `+factCols+` FROM fact WHERE id = ?`, id))
}
