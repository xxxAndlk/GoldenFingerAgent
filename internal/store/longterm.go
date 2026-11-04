package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// LongTermEpisode 是送入长期记忆抽取的原始片段。
type LongTermEpisode struct {
	SourceID      string
	OwnerUserID   string
	Text          string
	Source        string
	ReferenceTime time.Time
	Status        string
	Attempts      int
	LastError     string
	Embedding     []float32
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// 片段抽取状态。
const (
	LongTermStatusPending = "pending"
	LongTermStatusDone    = "done"
	LongTermStatusFailed  = "failed"
)

// LongTermEdge 是从片段抽取出的（主体, 关系, 客体）事实。
type LongTermEdge struct {
	ID            string
	OwnerUserID   string
	Subject       string
	Relation      string
	Object        string
	Fact          string
	ValidFrom     *time.Time
	ValidTo       *time.Time
	SourceRefs    []string
	Embedding     []float32
	Status        string
	ScopeType     string
	ScopeID       string
	ConditionText string
	Confidence    float64
	Version       int
	ConfirmedBy   string
	ConfirmedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// LongTermEdgeRepo 是 mem_episode / mem_edge 的持久化入口。
type LongTermEdgeRepo struct{ q Querier }

// NewLongTermRepo 构造长期记忆仓储。
func NewLongTermRepo(q Querier) *LongTermEdgeRepo { return &LongTermEdgeRepo{q: q} }

// InsertEpisode 幂等写入片段；已存在（含已被抽取）时返回 inserted=false。
func (r *LongTermEdgeRepo) InsertEpisode(ctx context.Context, e *LongTermEpisode) (bool, error) {
	now := nowForDB()
	res, err := r.q.ExecContext(ctx, `
		INSERT INTO mem_episode (source_id, owner_user_id, text, source, reference_time,
			status, attempts, last_error, embedding, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?)
		ON CONFLICT(source_id) DO NOTHING`,
		e.SourceID, e.OwnerUserID, e.Text, e.Source, e.ReferenceTime,
		LongTermStatusPending, vecToBlob(e.Embedding), now, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	e.Status = LongTermStatusPending
	e.CreatedAt, e.UpdatedAt = now, now
	return true, nil
}

// PendingEpisodes 取出待抽取片段（按时间顺序，保证先发生先抽取）。
func (r *LongTermEdgeRepo) PendingEpisodes(ctx context.Context, limit int) ([]LongTermEpisode, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT source_id, owner_user_id, text, source, reference_time, status, attempts,
			last_error, created_at, updated_at
		FROM mem_episode WHERE status = ? ORDER BY reference_time LIMIT ?`,
		LongTermStatusPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LongTermEpisode
	for rows.Next() {
		var e LongTermEpisode
		if err := rows.Scan(&e.SourceID, &e.OwnerUserID, &e.Text, &e.Source, &e.ReferenceTime,
			&e.Status, &e.Attempts, &e.LastError, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkEpisode 记录抽取结果；失败时保留片段以便重试。
func (r *LongTermEdgeRepo) MarkEpisode(ctx context.Context, sourceID, status, lastError string) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE mem_episode SET status = ?, attempts = attempts + 1, last_error = ?, updated_at = ?
		WHERE source_id = ?`, status, lastError, nowForDB(), sourceID)
	return err
}

// Episode 读取单个片段（用于测试与诊断）。
func (r *LongTermEdgeRepo) Episode(ctx context.Context, sourceID string) (LongTermEpisode, error) {
	var e LongTermEpisode
	err := r.q.QueryRowContext(ctx, `
		SELECT source_id, owner_user_id, text, source, reference_time, status, attempts,
			last_error, created_at, updated_at
		FROM mem_episode WHERE source_id = ?`, sourceID).
		Scan(&e.SourceID, &e.OwnerUserID, &e.Text, &e.Source, &e.ReferenceTime,
			&e.Status, &e.Attempts, &e.LastError, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// EpisodeCounts 返回按状态统计的片段数（供健康检查展示）。
func (r *LongTermEdgeRepo) EpisodeCounts(ctx context.Context) (pending, done, failed int, err error) {
	rows, qerr := r.q.QueryContext(ctx, `SELECT status, count(*) FROM mem_episode GROUP BY status`)
	if qerr != nil {
		return 0, 0, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return 0, 0, 0, err
		}
		switch status {
		case LongTermStatusPending:
			pending = n
		case LongTermStatusDone:
			done = n
		case LongTermStatusFailed:
			failed = n
		}
	}
	return pending, done, failed, rows.Err()
}

// InsertEdge 写入一条新抽取事实。
func (r *LongTermEdgeRepo) InsertEdge(ctx context.Context, e *LongTermEdge) error {
	refs, err := json.Marshal(e.SourceRefs)
	if err != nil {
		return err
	}
	if e.Status == "" {
		e.Status = MemoryInferred
	}
	if e.ScopeType == "" {
		e.ScopeType = "global"
	}
	if e.Confidence <= 0 {
		e.Confidence = 0.5
	}
	now := nowForDB()
	e.CreatedAt, e.UpdatedAt = now, now
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO mem_edge (id, owner_user_id, subject, relation, object, fact,
			valid_from, valid_to, source_refs, embedding, status, scope_type, scope_id,
			condition_text, confidence, version, confirmed_by, confirmed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		e.ID, e.OwnerUserID, e.Subject, e.Relation, e.Object, e.Fact,
		e.ValidFrom, e.ValidTo, string(refs), vecToBlob(e.Embedding), e.Status, e.ScopeType,
		e.ScopeID, e.ConditionText, e.Confidence, e.ConfirmedBy, e.ConfirmedAt, now, now)
	return err
}

// AddSourceRef 为已有事实补充来源引用（重复出现同一事实时使用）。
func (r *LongTermEdgeRepo) AddSourceRef(ctx context.Context, edgeID, sourceID string) error {
	var refs string
	err := r.q.QueryRowContext(ctx, `SELECT source_refs FROM mem_edge WHERE id = ?`, edgeID).Scan(&refs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var list []string
	if refs != "" {
		if err := json.Unmarshal([]byte(refs), &list); err != nil {
			return err
		}
	}
	if containsRef(list, sourceID) {
		return nil
	}
	merged, err := json.Marshal(append(list, sourceID))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `UPDATE mem_edge SET source_refs = ?, updated_at = ? WHERE id = ?`,
		string(merged), nowForDB(), edgeID)
	return err
}

// ActiveEdgesByTriple 找出同一 (主体, 关系) 下仍然成立的其他事实，用于时间失效。
func (r *LongTermEdgeRepo) ActiveEdgesByTriple(ctx context.Context, ownerID, subject, relation string) ([]LongTermEdge, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+longTermEdgeColumns+`
		FROM mem_edge
		WHERE owner_user_id = ? AND subject = ? AND relation = ? AND valid_to IS NULL AND deleted_at IS NULL`,
		ownerID, subject, relation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// InvalidateEdge 把旧事实标记为在 at 时刻失效（保留记录，不覆盖）。
func (r *LongTermEdgeRepo) InvalidateEdge(ctx context.Context, edgeID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE mem_edge SET valid_to = ?, updated_at = ? WHERE id = ? AND valid_to IS NULL`,
		at, nowForDB(), edgeID)
	return err
}

// Edges 列出某用户未删除的事实（含失效的，供历史查询与去重用）。
func (r *LongTermEdgeRepo) Edges(ctx context.Context, ownerID string, limit int) ([]LongTermEdge, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+longTermEdgeColumns+`
		FROM mem_edge WHERE owner_user_id = ? AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// EdgesBySubjects 取回与给定主体相关的事实（一跳邻居扩展）。
func (r *LongTermEdgeRepo) EdgesBySubjects(ctx context.Context, ownerID string, subjects []string, limit int) ([]LongTermEdge, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT ` + longTermEdgeColumns + `
		FROM mem_edge WHERE owner_user_id = ? AND deleted_at IS NULL AND subject IN (?`
	args := []any{ownerID, subjects[0]}
	for _, s := range subjects[1:] {
		query += ", ?"
		args = append(args, s)
	}
	query += `) ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// DeleteEdgesBySource 删除由某个片段派生出的事实，返回删除条数。
// 若事实同时被其他片段引用，则只移除该引用，不删除整条事实。
func (r *LongTermEdgeRepo) DeleteEdgesBySource(ctx context.Context, ownerID, sourceID string) (int, error) {
	edges, err := r.Edges(ctx, ownerID, 0)
	if err != nil {
		return 0, err
	}
	removed := 0
	now := nowForDB()
	for _, edge := range edges {
		if !containsRef(edge.SourceRefs, sourceID) {
			continue
		}
		remaining := removeRef(edge.SourceRefs, sourceID)
		if len(remaining) > 0 {
			refs, err := json.Marshal(remaining)
			if err != nil {
				return removed, err
			}
			if _, err := r.q.ExecContext(ctx,
				`UPDATE mem_edge SET source_refs = ?, updated_at = ? WHERE id = ?`,
				string(refs), now, edge.ID); err != nil {
				return removed, err
			}
			continue
		}
		if _, err := r.q.ExecContext(ctx,
			`UPDATE mem_edge SET deleted_at = ?, updated_at = ? WHERE id = ?`,
			now, now, edge.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// DeleteEdge 硬删除一条事实（用户明确要求遗忘时）。
func (r *LongTermEdgeRepo) DeleteEdge(ctx context.Context, ownerID, edgeID string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM mem_edge WHERE id = ? AND owner_user_id = ?`, edgeID, ownerID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEpisode 删除单个片段记录，使其不再被抽取或重试。
func (r *LongTermEdgeRepo) DeleteEpisode(ctx context.Context, sourceID string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM mem_episode WHERE source_id = ?`, sourceID)
	return err
}

// DeleteEpisodesByOwner 在遗忘整人/整账号时清理长期记忆片段。
func (r *LongTermEdgeRepo) DeleteEpisodesByOwner(ctx context.Context, ownerID string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM mem_episode WHERE owner_user_id = ?`, ownerID)
	return err
}

const longTermEdgeColumns = `id, owner_user_id, subject, relation, object, fact,
	valid_from, valid_to, source_refs, embedding, status, scope_type, scope_id,
	condition_text, confidence, version, confirmed_by, confirmed_at, created_at, updated_at`

func scanEdges(rows *sql.Rows) ([]LongTermEdge, error) {
	var out []LongTermEdge
	for rows.Next() {
		var e LongTermEdge
		var refs string
		var emb []byte
		var confirmedAt sql.NullTime
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Subject, &e.Relation, &e.Object, &e.Fact,
			&e.ValidFrom, &e.ValidTo, &refs, &emb, &e.Status, &e.ScopeType, &e.ScopeID,
			&e.ConditionText, &e.Confidence, &e.Version, &e.ConfirmedBy, &confirmedAt,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.ConfirmedAt = timePointer(confirmedAt)
		if refs != "" {
			if err := json.Unmarshal([]byte(refs), &e.SourceRefs); err != nil {
				return nil, err
			}
		}
		vec, err := parseBlob(emb)
		if err != nil {
			return nil, err
		}
		e.Embedding = vec
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *LongTermEdgeRepo) GetEdge(ctx context.Context, ownerID, id string) (*LongTermEdge, error) {
	var e LongTermEdge
	var refs string
	var emb []byte
	var confirmedAt sql.NullTime
	err := r.q.QueryRowContext(ctx, `SELECT `+longTermEdgeColumns+` FROM mem_edge
		WHERE owner_user_id = ? AND id = ? AND deleted_at IS NULL`, ownerID, id).
		Scan(&e.ID, &e.OwnerUserID, &e.Subject, &e.Relation, &e.Object, &e.Fact,
			&e.ValidFrom, &e.ValidTo, &refs, &emb, &e.Status, &e.ScopeType, &e.ScopeID,
			&e.ConditionText, &e.Confidence, &e.Version, &e.ConfirmedBy, &confirmedAt,
			&e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.ConfirmedAt = timePointer(confirmedAt)
	if refs != "" {
		_ = json.Unmarshal([]byte(refs), &e.SourceRefs)
	}
	e.Embedding, _ = parseBlob(emb)
	return &e, nil
}

// SetEdgeStatus 以版本 CAS 更新确认状态；inferred 永远不能直接覆盖 confirmed。
func (r *LongTermEdgeRepo) SetEdgeStatus(ctx context.Context, ownerID, id, status string,
	expectedVersion int, confirmedBy string, at time.Time) (bool, error) {
	var confirmed any
	if status == MemoryConfirmed {
		confirmed = at
	}
	res, err := r.q.ExecContext(ctx, `UPDATE mem_edge SET status = ?, confirmed_by = ?,
		confirmed_at = ?, version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND version = ? AND deleted_at IS NULL`,
		status, confirmedBy, confirmed, at, ownerID, id, expectedVersion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func containsRef(refs []string, target string) bool {
	for _, r := range refs {
		if r == target {
			return true
		}
	}
	return false
}

func removeRef(refs []string, target string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r != target {
			out = append(out, r)
		}
	}
	return out
}
