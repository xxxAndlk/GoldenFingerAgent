package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ---- 收件箱事件 ----

// InboxEvent 旁路上报的通知事件；kind=normal|clue，线索见 clue_type/clue_value。
type InboxEvent struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"owner_user_id"`
	SourcePkg   string    `json:"source_pkg"`
	AppName     string    `json:"app_name"`
	Title       string    `json:"title"`
	Text        string    `json:"text"`
	Kind        string    `json:"kind"`
	ClueType    string    `json:"clue_type"`
	ClueValue   string    `json:"clue_value"`
	PostedAt    time.Time `json:"posted_at"`
	IngestedAt  time.Time `json:"ingested_at"`
	DedupeHash  string    `json:"-"`
	Embedding   []float32 `json:"-"`
}

// ScoredInboxEvent 是带相似度得分的事件（语义检索结果）。
type ScoredInboxEvent struct {
	InboxEvent
	Score float64 `json:"score"`
}

// 可空 TEXT 列统一 COALESCE；时间列保持裸列名（保留 DATETIME 声明供驱动
// 转换 time.Time，表达式会丢失声明导致 string Scan 失败），NULL 由指针处理。
const inboxEventCols = `id, owner_user_id, COALESCE(source_pkg,''), COALESCE(app_name,''),` +
	` COALESCE(title,''), COALESCE(text,''), kind, COALESCE(clue_type,''), COALESCE(clue_value,''),` +
	` posted_at, ingested_at`

func scanInboxEvent(scan func(dest ...any) error) (InboxEvent, error) {
	var e InboxEvent
	var postedAt *time.Time
	err := scan(&e.ID, &e.OwnerUserID, &e.SourcePkg, &e.AppName, &e.Title, &e.Text,
		&e.Kind, &e.ClueType, &e.ClueValue, &postedAt, &e.IngestedAt)
	if err != nil {
		return e, err
	}
	if postedAt != nil {
		e.PostedAt = *postedAt
	} else {
		e.PostedAt = e.IngestedAt
	}
	return e, nil
}

type InboxEventRepo struct{ q Querier }

func NewInboxEventRepo(q Querier) *InboxEventRepo { return &InboxEventRepo{q: q} }

// Insert 尝试写入事件；命中 UNIQUE(owner_user_id, dedupe_hash) 时忽略并返回 false。
func (r *InboxEventRepo) Insert(ctx context.Context, e *InboxEvent) (bool, error) {
	e.ID = uuid.NewString()
	if e.IngestedAt.IsZero() {
		e.IngestedAt = nowForDB()
	}
	if e.Kind == "" {
		e.Kind = "normal"
	}
	res, err := r.q.ExecContext(ctx, `INSERT OR IGNORE INTO inbox_event
		(id, owner_user_id, source_pkg, app_name, title, text, kind, clue_type, clue_value,
		 posted_at, ingested_at, dedupe_hash, embedding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.OwnerUserID, e.SourcePkg, e.AppName, e.Title, e.Text, e.Kind,
		e.ClueType, e.ClueValue, e.PostedAt, e.IngestedAt, e.DedupeHash, vecToBlob(e.Embedding))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *InboxEventRepo) Get(ctx context.Context, id string) (*InboxEvent, error) {
	e, err := scanInboxEvent(r.q.QueryRowContext(ctx,
		`SELECT `+inboxEventCols+` FROM inbox_event WHERE id = ?`, id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &e, nil
}

// List 按 kind（normal|clue|其余=all）返回最近事件，posted_at 倒序。
func (r *InboxEventRepo) List(ctx context.Context, ownerID, kind string, limit int) ([]InboxEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + inboxEventCols + ` FROM inbox_event WHERE owner_user_id = ?`
	args := []any{ownerID}
	if kind == "normal" || kind == "clue" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY posted_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxEvent
	for rows.Next() {
		e, err := scanInboxEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SearchKeyword 关键词降级检索：title/text LIKE（转义 LIKE 通配符）。
func (r *InboxEventRepo) SearchKeyword(ctx context.Context, ownerID, q string, limit int) ([]InboxEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	pat := "%" + escapeLike(q) + "%"
	rows, err := r.q.QueryContext(ctx, `SELECT `+inboxEventCols+` FROM inbox_event
		WHERE owner_user_id = ? AND (title LIKE ? ESCAPE '\' OR text LIKE ? ESCAPE '\')
		ORDER BY posted_at DESC LIMIT ?`, ownerID, pat, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxEvent
	for rows.Next() {
		e, err := scanInboxEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Similar 取出带向量的事件，在 Go 内算余弦并排序取前 k（参照 EpisodeRepo.Similar）。
func (r *InboxEventRepo) Similar(ctx context.Context, ownerID string, vec []float32, k int) ([]ScoredInboxEvent, error) {
	if len(vec) == 0 {
		return nil, nil
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+inboxEventCols+`, embedding FROM inbox_event
		WHERE owner_user_id = ? AND embedding IS NOT NULL`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScoredInboxEvent
	for rows.Next() {
		var emb []byte
		e, err := scanInboxEvent(func(dest ...any) error {
			return rows.Scan(append(dest, &emb)...)
		})
		if err != nil {
			return nil, err
		}
		got, err := parseBlob(emb)
		if err != nil || len(got) != len(vec) {
			continue // 维度不符/损坏向量跳过
		}
		out = append(out, ScoredInboxEvent{InboxEvent: e, Score: float64(cosine(vec, got))})
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

// SetEmbedding 回填事件向量（embed 失败时不调用，保持 NULL）。
func (r *InboxEventRepo) SetEmbedding(ctx context.Context, id string, vec []float32) error {
	_, err := r.q.ExecContext(ctx, `UPDATE inbox_event SET embedding = ? WHERE id = ?`, vecToBlob(vec), id)
	return err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ---- 稍后处理（snooze） ----

type InboxSnooze struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"owner_user_id"`
	EventID     string    `json:"event_id"`
	FireAt      time.Time `json:"fire_at"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// InboxSnoozeView 关联事件原文的稍后处理视图（GET /api/inbox/snooze）。
type InboxSnoozeView struct {
	ID      string    `json:"id"`
	EventID string    `json:"event_id"`
	Title   string    `json:"title"`
	Text    string    `json:"text"`
	FireAt  time.Time `json:"fire_at"`
	Status  string    `json:"status"`
}

type InboxSnoozeRepo struct{ q Querier }

func NewInboxSnoozeRepo(q Querier) *InboxSnoozeRepo { return &InboxSnoozeRepo{q: q} }

func (r *InboxSnoozeRepo) Insert(ctx context.Context, s *InboxSnooze) error {
	s.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `INSERT INTO inbox_snooze
		(id, owner_user_id, event_id, fire_at, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING created_at`,
		s.ID, s.OwnerUserID, s.EventID, s.FireAt, s.Status, nowForDB()).Scan(&s.CreatedAt)
}

// DuePending 返回全部用户已到期仍为 pending 的登记（全局扫描，供后台轮询）。
func (r *InboxSnoozeRepo) DuePending(ctx context.Context, now time.Time) ([]InboxSnooze, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, COALESCE(owner_user_id,''), event_id, fire_at, status, created_at
		FROM inbox_snooze WHERE status = 'pending' AND fire_at <= ? ORDER BY fire_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxSnooze
	for rows.Next() {
		var s InboxSnooze
		if err := rows.Scan(&s.ID, &s.OwnerUserID, &s.EventID, &s.FireAt, &s.Status, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkFired 将到期项置为 fired（仅 pending 生效，防止并发下重复触发）。
func (r *InboxSnoozeRepo) MarkFired(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	_, err := r.q.ExecContext(ctx,
		`UPDATE inbox_snooze SET status = 'fired' WHERE status = 'pending' AND id IN (`+ph+`)`, args...)
	return err
}

// ListWithEvent 按用户列出登记并关联事件标题/原文。
func (r *InboxSnoozeRepo) ListWithEvent(ctx context.Context, ownerID string) ([]InboxSnoozeView, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT s.id, s.event_id, COALESCE(e.title,''), COALESCE(e.text,''),
		s.fire_at, s.status
		FROM inbox_snooze s LEFT JOIN inbox_event e ON e.id = s.event_id
		WHERE s.owner_user_id = ? ORDER BY s.fire_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxSnoozeView
	for rows.Next() {
		var v InboxSnoozeView
		if err := rows.Scan(&v.ID, &v.EventID, &v.Title, &v.Text, &v.FireAt, &v.Status); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- 关注规则（watch） ----

type InboxWatch struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"owner_user_id"`
	Title       string    `json:"title"`
	Keyword     string    `json:"keyword"`
	SourcePkg   string    `json:"source_pkg"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

const inboxWatchCols = `id, COALESCE(owner_user_id,''), COALESCE(title,''), keyword, COALESCE(source_pkg,''), status, created_at`

func scanInboxWatch(scan func(dest ...any) error) (InboxWatch, error) {
	var w InboxWatch
	err := scan(&w.ID, &w.OwnerUserID, &w.Title, &w.Keyword, &w.SourcePkg, &w.Status, &w.CreatedAt)
	return w, err
}

type InboxWatchRepo struct{ q Querier }

func NewInboxWatchRepo(q Querier) *InboxWatchRepo { return &InboxWatchRepo{q: q} }

func (r *InboxWatchRepo) Insert(ctx context.Context, w *InboxWatch) error {
	w.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `INSERT INTO inbox_watch
		(id, owner_user_id, title, keyword, source_pkg, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING created_at`,
		w.ID, w.OwnerUserID, w.Title, w.Keyword, w.SourcePkg, w.Status, nowForDB()).Scan(&w.CreatedAt)
}

// List 返回用户全部 watch（含已 fired/cancelled，供面板展示）。
func (r *InboxWatchRepo) List(ctx context.Context, ownerID string) ([]InboxWatch, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+inboxWatchCols+` FROM inbox_watch
		WHERE owner_user_id = ? ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxWatch
	for rows.Next() {
		w, err := scanInboxWatch(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListArmed 仅返回待命中的规则。
func (r *InboxWatchRepo) ListArmed(ctx context.Context, ownerID string) ([]InboxWatch, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+inboxWatchCols+` FROM inbox_watch
		WHERE owner_user_id = ? AND status = 'armed' ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxWatch
	for rows.Next() {
		w, err := scanInboxWatch(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkFired 命中后置 fired（仅 armed 生效）。
func (r *InboxWatchRepo) MarkFired(ctx context.Context, id string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE inbox_watch SET status = 'fired' WHERE id = ? AND status = 'armed'`, id)
	return err
}

// Cancel 软取消；返回 false 表示不存在或已取消。
func (r *InboxWatchRepo) Cancel(ctx context.Context, ownerID, id string) (bool, error) {
	res, err := r.q.ExecContext(ctx,
		`UPDATE inbox_watch SET status = 'cancelled' WHERE id = ? AND owner_user_id = ? AND status <> 'cancelled'`,
		id, ownerID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
