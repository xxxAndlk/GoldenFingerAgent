package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/google/uuid"
)

type EpisodeRepo struct{ q Querier }

func NewEpisodeRepo(q Querier) *EpisodeRepo { return &EpisodeRepo{q: q} }

func (r *EpisodeRepo) Create(ctx context.Context, e *Episode) error {
	e.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `
		INSERT INTO episode (id, owner_user_id, summary, raw_ref, time_start, time_end,
			embedding, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at`,
		e.ID, e.OwnerUserID, e.Summary, e.RawRef, e.TimeStart, e.TimeEnd,
		vecToBlob(e.Embedding), e.ExpiresAt, nowForDB(),
	).Scan(&e.CreatedAt)
}

// Similar 取出候选（含 embedding BLOB），在 Go 内算余弦并排序取前 k。
func (r *EpisodeRepo) Similar(ctx context.Context, ownerID string, vec []float32, k int) ([]ScoredEpisode, error) {
	if len(vec) == 0 {
		return nil, nil
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, owner_user_id, summary, raw_ref, time_start, time_end,
			expires_at, created_at, deleted_at, embedding
		FROM episode
		WHERE owner_user_id = ? AND deleted_at IS NULL AND embedding IS NOT NULL`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScoredEpisode
	for rows.Next() {
		var e Episode
		var emb []byte
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Summary, &e.RawRef, &e.TimeStart, &e.TimeEnd,
			&e.ExpiresAt, &e.CreatedAt, &e.DeletedAt, &emb); err != nil {
			return nil, err
		}
		got, err := parseBlob(emb)
		if err != nil || len(got) != len(vec) {
			continue // 维度不符/损坏的向量跳过
		}
		out = append(out, ScoredEpisode{Episode: e, Score: float64(cosine(vec, got))})
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

// ClearPersonRefs 在遗忘人物时清除片段里指向该人物的原始引用文本。
func (r *EpisodeRepo) ClearPersonRefs(ctx context.Context, ownerID, personName string) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE episode SET summary = replace(summary, ?, '')
		WHERE owner_user_id = ? AND deleted_at IS NULL`, personName, ownerID)
	return err
}

// ListRecent 返回最近的片段（供调试/面板）。
func (r *EpisodeRepo) ListRecent(ctx context.Context, ownerID string, limit int) ([]Episode, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, owner_user_id, summary, raw_ref, time_start, time_end,
			expires_at, created_at, deleted_at
		FROM episode
		WHERE owner_user_id = ? AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Summary, &e.RawRef, &e.TimeStart, &e.TimeEnd,
			&e.ExpiresAt, &e.CreatedAt, &e.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HardDeleteByOwner 删除某用户全部片段（用户注销时清理）。
func (r *EpisodeRepo) HardDeleteByOwner(ctx context.Context, ownerID string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM episode WHERE owner_user_id = ?`, ownerID)
	return err
}

// Get 返回单条片段。
func (r *EpisodeRepo) Get(ctx context.Context, id string) (*Episode, error) {
	e := &Episode{}
	err := r.q.QueryRowContext(ctx, `
		SELECT id, owner_user_id, summary, raw_ref, time_start, time_end,
			expires_at, created_at, deleted_at
		FROM episode WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&e.ID, &e.OwnerUserID, &e.Summary, &e.RawRef, &e.TimeStart, &e.TimeEnd,
			&e.ExpiresAt, &e.CreatedAt, &e.DeletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return e, nil
}
