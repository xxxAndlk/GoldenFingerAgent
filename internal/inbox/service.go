// Package inbox 实现通知中心领域服务：上报去重、线索提取、关注（watch）
// 匹配、稍后处理（snooze）到点与语义/关键词检索。LLM 仅参与向量化。
package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
)

// Service 通知中心领域服务。Embedder 可为 nil（检索降级关键词模式）。
type Service struct {
	Repos    *store.Repos
	Embedder llm.Embedder
	Now      func() time.Time
	Dim      int
	// Notify 在 watch 命中时回调（装配层接 Outbox.Push）；nil 时静默跳过。
	Notify func(ctx context.Context, ownerID, body string)
}

func NewService(repos *store.Repos, emb llm.Embedder, now func() time.Time, dim int) *Service {
	if now == nil {
		now = time.Now
	}
	if dim <= 0 {
		dim = 1024
	}
	return &Service{Repos: repos, Embedder: emb, Now: now, Dim: dim}
}

// Input 是一条旁路上报的通知（JSON 字段与跨域契约一致）。
type Input struct {
	SourcePkg string    `json:"source_pkg"`
	AppName   string    `json:"app_name"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	PostedAt  time.Time `json:"posted_at"`
}

// DueItem 是到期的稍后处理项，由调用方推送 Outbox。
type DueItem struct {
	OwnerUserID string
	Body        string
}

// Outcome 携带检索模式（vector|keyword，如实回传禁止静默降级）与结果。
type Outcome struct {
	Mode   string
	Events []store.ScoredInboxEvent
}

// Ingest 批量入库：去重 → 线索提取 → watch 匹配 → 向量化（失败保持 NULL）。
// 返回实际入库条数（重复上报不计）。
func (s *Service) Ingest(ctx context.Context, ownerID string, inputs []Input) (int, error) {
	inserted := 0
	for _, in := range inputs {
		if in.PostedAt.IsZero() {
			in.PostedAt = s.Now()
		}
		title, text := strings.TrimSpace(in.Title), strings.TrimSpace(in.Text)
		combined := title + "\n" + text
		clueType, clueValue := ExtractClue(combined)
		kind := "normal"
		if clueType != "" {
			kind = "clue"
		}
		ev := &store.InboxEvent{
			OwnerUserID: ownerID,
			SourcePkg:   strings.TrimSpace(in.SourcePkg),
			AppName:     strings.TrimSpace(in.AppName),
			Title:       title,
			Text:        text,
			Kind:        kind,
			ClueType:    clueType,
			ClueValue:   clueValue,
			PostedAt:    in.PostedAt,
			DedupeHash:  dedupeHash(in.SourcePkg, title, text, in.PostedAt),
		}
		ok, err := s.Repos.InboxEvents.Insert(ctx, ev)
		if err != nil {
			return inserted, err
		}
		if !ok {
			continue // 重复上报，静默去重
		}
		inserted++
		s.matchWatches(ctx, ownerID, ev, combined)
		s.embedEvent(ctx, ev.ID, combined)
	}
	return inserted, nil
}

// dedupeHash 按包名+标题+正文+posted_at 分钟桶生成去重键。
func dedupeHash(pkg, title, text string, postedAt time.Time) string {
	bucket := postedAt.Format("2006-01-02T15:04")
	sum := sha256.Sum256([]byte(pkg + "\x1f" + title + "\x1f" + text + "\x1f" + bucket))
	return hex.EncodeToString(sum[:])
}

// matchWatches 用新事件匹配 armed watch：非空字段全部需命中（source_pkg 为空通配）。
func (s *Service) matchWatches(ctx context.Context, ownerID string, ev *store.InboxEvent, combined string) {
	watches, err := s.Repos.InboxWatches.ListArmed(ctx, ownerID)
	if err != nil || len(watches) == 0 {
		return
	}
	for _, w := range watches {
		if w.SourcePkg != "" && w.SourcePkg != ev.SourcePkg {
			continue
		}
		if w.Title != "" && !strings.Contains(combined, w.Title) {
			continue
		}
		if w.Keyword != "" && !strings.Contains(combined, w.Keyword) {
			continue
		}
		if w.Title == "" && w.Keyword == "" {
			continue
		}
		if err := s.Repos.InboxWatches.MarkFired(ctx, w.ID); err != nil {
			continue
		}
		if s.Notify != nil {
			label := w.Title
			if label == "" {
				label = w.Keyword
			}
			s.Notify(ctx, ownerID, "等到了："+label+"\n"+ev.AppName+": "+ev.Title+" "+truncateRunes(ev.Text, 80))
		}
	}
}

// embedEvent 尝试向量化并回填；失败保持 embedding=NULL 且不报错。
func (s *Service) embedEvent(ctx context.Context, eventID, text string) {
	if s.Embedder == nil {
		return
	}
	vecs, err := s.Embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		return
	}
	_ = s.Repos.InboxEvents.SetEmbedding(ctx, eventID, vecs[0])
}

// Due 返回全部到期登记并置 fired；Body 按契约模板生成，推送由调用方完成。
func (s *Service) Due(ctx context.Context, now time.Time) ([]DueItem, error) {
	due, err := s.Repos.InboxSnoozes.DuePending(ctx, now)
	if err != nil {
		return nil, err
	}
	if len(due) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(due))
	items := make([]DueItem, 0, len(due))
	for _, sn := range due {
		ids = append(ids, sn.ID)
		ev, err := s.Repos.InboxEvents.Get(ctx, sn.EventID)
		if err != nil {
			continue // 事件已被清理：仍置 fired，避免反复触发
		}
		items = append(items, DueItem{
			OwnerUserID: sn.OwnerUserID,
			Body:        "该看了：" + ev.AppName + "\n" + ev.Title + " " + ev.Text,
		})
	}
	if err := s.Repos.InboxSnoozes.MarkFired(ctx, ids); err != nil {
		return items, err
	}
	return items, nil
}

// Search 语义检索：embed 查询并余弦排序；无 embedder/embed 失败/空查询 →
// 关键词 LIKE。mode 如实回传。
func (s *Service) Search(ctx context.Context, ownerID, q string, limit int) (*Outcome, error) {
	if limit <= 0 {
		limit = 20
	}
	q = strings.TrimSpace(q)
	if s.Embedder != nil && q != "" {
		vecs, err := s.Embedder.Embed(ctx, []string{q})
		if err == nil && len(vecs) > 0 && len(vecs[0]) > 0 {
			hits, err := s.Repos.InboxEvents.Similar(ctx, ownerID, vecs[0], limit)
			if err != nil {
				return nil, err
			}
			return &Outcome{Mode: "vector", Events: hits}, nil
		}
	}
	hits, err := s.Repos.InboxEvents.SearchKeyword(ctx, ownerID, q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]store.ScoredInboxEvent, len(hits))
	for i, e := range hits {
		out[i] = store.ScoredInboxEvent{InboxEvent: e}
	}
	return &Outcome{Mode: "keyword", Events: out}, nil
}

// CreateSnooze 登记稍后处理：minutes 优先，其次显式 fire_at；事件必须存在。
func (s *Service) CreateSnooze(ctx context.Context, ownerID, eventID string, minutes int, fireAt *time.Time) (*store.InboxSnooze, error) {
	if _, err := s.Repos.InboxEvents.Get(ctx, eventID); err != nil {
		return nil, fmt.Errorf("load event: %w", err)
	}
	sn := &store.InboxSnooze{OwnerUserID: ownerID, EventID: eventID, Status: "pending"}
	switch {
	case minutes > 0:
		sn.FireAt = s.Now().Add(time.Duration(minutes) * time.Minute)
	case fireAt != nil && !fireAt.IsZero():
		sn.FireAt = *fireAt
	default:
		return nil, errors.New("minutes or fire_at required")
	}
	if err := s.Repos.InboxSnoozes.Insert(ctx, sn); err != nil {
		return nil, err
	}
	return sn, nil
}

// CreateSnoozeByMatch 按“包名 + 标题 + 正文”匹配最近一条通知再延后提醒。
// 手机端只需要把通知里的字段原样上报，不必自己拉列表做匹配
// （原先 Kotlin 的 InboxApi 就是这么做的，把它搬回后端）。
func (s *Service) CreateSnoozeByMatch(ctx context.Context, ownerID, pkg, title, text string, minutes int, fireAt *time.Time) (*store.InboxSnooze, error) {
	pkg = strings.TrimSpace(pkg)
	title = strings.TrimSpace(title)
	text = strings.TrimSpace(text)
	if pkg == "" && title == "" && text == "" {
		return nil, errors.New("pkg/title/text 至少要有一项")
	}
	events, err := s.Repos.InboxEvents.List(ctx, ownerID, "all", 50)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if pkg != "" && !strings.EqualFold(e.SourcePkg, pkg) {
			continue
		}
		if title != "" && !strings.Contains(e.Title, title) {
			continue
		}
		if text != "" && !strings.Contains(e.Text, text) {
			continue
		}
		return s.CreateSnooze(ctx, ownerID, e.ID, minutes, fireAt)
	}
	return nil, store.ErrNotFound
}

// CreateWatch 新增关注规则；title 与 keyword 至少一个非空。
func (s *Service) CreateWatch(ctx context.Context, ownerID, title, keyword, sourcePkg string) (*store.InboxWatch, error) {
	title, keyword, sourcePkg = strings.TrimSpace(title), strings.TrimSpace(keyword), strings.TrimSpace(sourcePkg)
	if title == "" && keyword == "" {
		return nil, errors.New("title or keyword required")
	}
	w := &store.InboxWatch{
		OwnerUserID: ownerID, Title: title, Keyword: keyword, SourcePkg: sourcePkg, Status: "armed",
	}
	if err := s.Repos.InboxWatches.Insert(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

// CancelWatch 取消关注规则；返回 false 表示不存在或已取消。
func (s *Service) CancelWatch(ctx context.Context, ownerID, id string) (bool, error) {
	return s.Repos.InboxWatches.Cancel(ctx, ownerID, id)
}

// truncateRunes 按 rune 截断并加省略号（watch 推送正文用）。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
