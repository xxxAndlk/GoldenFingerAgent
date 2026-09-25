package store

import (
	"context"
)

type EpisodeRepo struct{ q Querier }

func NewEpisodeRepo(q Querier) *EpisodeRepo { return &EpisodeRepo{q: q} }

func (r *EpisodeRepo) Create(ctx context.Context, e *Episode) error {
	var emb any
	if len(e.Embedding) > 0 {
		emb = vecToString(e.Embedding)
	}
	return r.q.QueryRow(ctx, `
		INSERT INTO episode (owner_user_id, summary, raw_ref, time_range, embedding, expires_at)
		VALUES ($1, $2, $3,
			CASE WHEN $4::timestamptz IS NULL OR $5::timestamptz IS NULL
			     THEN NULL ELSE tstzrange($4::timestamptz, $5::timestamptz) END,
			$6::vector, $7)
		RETURNING id, created_at`,
		e.OwnerUserID, e.Summary, e.RawRef, e.TimeStart, e.TimeEnd, emb, e.ExpiresAt,
	).Scan(&e.ID, &e.CreatedAt)
}

func (r *EpisodeRepo) Similar(ctx context.Context, ownerID string, vec []float32, k int) ([]ScoredEpisode, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, owner_user_id, summary, raw_ref,
			lower(time_range), upper(time_range), expires_at, created_at, deleted_at,
			1 - (embedding <=> $2::vector) AS score
		FROM episode
		WHERE owner_user_id = $1 AND deleted_at IS NULL AND embedding IS NOT NULL
		  AND (expires_at IS NULL OR expires_at > now())
		ORDER BY embedding <=> $2::vector
		LIMIT $3`, ownerID, vecToString(vec), k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScoredEpisode
	for rows.Next() {
		var e Episode
		var score float64
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Summary, &e.RawRef,
			&e.TimeStart, &e.TimeEnd, &e.ExpiresAt, &e.CreatedAt, &e.DeletedAt, &score); err != nil {
			return nil, err
		}
		out = append(out, ScoredEpisode{Episode: e, Score: score})
	}
	return out, rows.Err()
}

// ClearPersonRefs rewrites episode summaries to drop a forgotten person's name.
func (r *EpisodeRepo) ClearPersonRefs(ctx context.Context, ownerID, personName string) error {
	_, err := r.q.Exec(ctx, `
		UPDATE episode SET summary = replace(summary, $3, '[已遗忘]')
		WHERE owner_user_id = $1 AND deleted_at IS NULL AND summary LIKE '%' || $3 || '%'`,
		ownerID, personName)
	return err
}

func (r *EpisodeRepo) HardDeleteByOwner(ctx context.Context, ownerID string) error {
	_, err := r.q.Exec(ctx, `DELETE FROM episode WHERE owner_user_id = $1`, ownerID)
	return err
}

// ListRecent returns the latest episodes for a user (context building).
func (r *EpisodeRepo) ListRecent(ctx context.Context, ownerID string, limit int) ([]Episode, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, owner_user_id, summary, raw_ref,
			lower(time_range), upper(time_range), expires_at, created_at, deleted_at
		FROM episode
		WHERE owner_user_id = $1 AND deleted_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		ORDER BY created_at DESC LIMIT $2`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Summary, &e.RawRef,
			&e.TimeStart, &e.TimeEnd, &e.ExpiresAt, &e.CreatedAt, &e.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
