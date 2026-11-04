package memory

import (
	"context"
	"log"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"goldenfinger/agent/internal/store"
)

// Snippet 是一个可检索的记忆条目（事实或片段）及其排序得分。
// 情景片段额外携带发生时间和原始证据，供回答时核对上下文。
type Snippet struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"` // "fact" | "episode"
	Text       string     `json:"text"`
	Person     string     `json:"person,omitempty"`
	Score      float64    `json:"score"`
	Status     string     `json:"status,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	RawRef     string     `json:"raw_ref,omitempty"`
}

type rankedSnippet struct {
	snippet Snippet
	vector  float64
	lexical float64
	at      time.Time
}

// Search 融合向量、关键词和时效性。embedder 缺失或失败时，关键词检索仍可用。
func (s *Service) Search(ctx context.Context, ownerID, query string, k int) ([]Snippet, error) {
	if k <= 0 {
		k = 8
	}
	items := make(map[string]*rankedSnippet)
	var queryVec []float32
	if s.embed != nil {
		vecs, err := s.embed.Embed(ctx, []string{query})
		if err == nil && len(vecs) == 1 {
			queryVec = vecs[0]
		}
	}

	if len(queryVec) > 0 {
		facts, err := s.facts.Similar(ctx, ownerID, queryVec, k)
		if err != nil {
			return nil, err
		}
		for _, f := range facts {
			items[f.ID] = &rankedSnippet{
				snippet: Snippet{ID: f.ID, Kind: "fact", Text: f.FactType + ": " + f.ValueText, Status: f.Status},
				vector:  f.Score, at: f.UpdatedAt,
			}
		}
		eps, err := s.eps.Similar(ctx, ownerID, queryVec, k)
		if err != nil {
			return nil, err
		}
		for _, e := range eps {
			at := episodeTime(e.Episode)
			items[e.ID] = &rankedSnippet{
				snippet: episodeSnippet(e.Episode, at), vector: e.Score, at: at,
			}
		}
	}

	// 对最近候选做关键词评分；它既是向量召回的补充，也是无向量时的降级路径。
	facts, err := s.facts.ListCurrentByOwner(ctx, ownerID, 200)
	if err != nil {
		return nil, err
	}
	for _, f := range facts {
		text := f.FactType + ": " + f.ValueText
		item := items[f.ID]
		if item == nil {
			item = &rankedSnippet{snippet: Snippet{ID: f.ID, Kind: "fact", Text: text, Status: f.Status}, at: f.UpdatedAt}
			items[f.ID] = item
		}
		item.lexical = lexicalScore(query, text)
	}
	eps, err := s.eps.ListRecent(ctx, ownerID, 200)
	if err != nil {
		return nil, err
	}
	for _, e := range eps {
		at := episodeTime(e)
		item := items[e.ID]
		if item == nil {
			item = &rankedSnippet{snippet: episodeSnippet(e, at), at: at}
			items[e.ID] = item
		}
		item.lexical = lexicalScore(query, e.Summary)
	}

	// 可替换长期记忆后端提供跨片段事实；不可用时仅降级，不影响 SQLite 检索。
	includeHistory := historicalQuery(query)
	if s.longTerm != nil {
		facts, err := s.longTerm.Search(ctx, MemoryQuery{OwnerID: ownerID, Query: query, Limit: k})
		if err != nil {
			log.Printf("[memory] long-term search deferred: %v", err)
		} else {
			for _, fact := range facts {
				if strings.TrimSpace(fact.ID) == "" || strings.TrimSpace(fact.Text) == "" {
					continue
				}
				historical := fact.InvalidAt != nil
				if historical && !includeHistory {
					continue
				}
				at := time.Time{}
				if fact.ValidAt != nil {
					at = *fact.ValidAt
				}
				status := fact.Status
				if historical {
					status = "historical"
				}
				// ltm 是 GoldenFinger 内部命名空间，业务层不感知后端 UUID 类型。
				id := "ltm:" + fact.ID
				items[id] = &rankedSnippet{
					snippet: Snippet{
						ID: id, Kind: "fact", Text: fact.Text, Status: status,
						OccurredAt: fact.ValidAt, RawRef: strings.Join(fact.SourceRefs, ","),
					},
					vector: 0.85, lexical: lexicalScore(query, fact.Text), at: at,
				}
			}
		}
	}

	broadQuery := normalizeRecallText(query) == ""
	halfLife := s.recencyHalfLifeValue()
	out := make([]Snippet, 0, len(items))
	for _, item := range items {
		if !broadQuery && item.lexical < 0.12 && item.vector <= 0 {
			continue
		}
		recency := recencyScore(s.now(), item.at, halfLife)
		if len(queryVec) > 0 || item.vector > 0 {
			item.snippet.Score = 0.55*item.vector + 0.35*item.lexical + 0.10*recency
		} else {
			item.snippet.Score = 0.85*item.lexical + 0.15*recency
		}
		out = append(out, item.snippet)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func historicalQuery(query string) bool {
	for _, marker := range []string{"以前", "之前", "历史", "过去", "曾经", "原来"} {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return false
}

func episodeSnippet(e store.Episode, at time.Time) Snippet {
	return Snippet{ID: e.ID, Kind: "episode", Text: e.Summary, OccurredAt: &at, RawRef: e.RawRef}
}

func episodeTime(e store.Episode) time.Time {
	if e.TimeStart != nil {
		return *e.TimeStart
	}
	return e.CreatedAt
}

func recencyScore(now, at time.Time, halfLife time.Duration) float64 {
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	if halfLife <= 0 {
		halfLife = 30 * 24 * time.Hour
	}
	return math.Exp(-math.Ln2 * float64(age) / float64(halfLife))
}

// lexicalScore 用查询二元字组覆盖率衡量匹配程度。对中文口语查询而言，
// 去掉“前几天/我说过/哪个”等问句外壳后，原句中的关键短语仍能直接命中。
func lexicalScore(query, candidate string) float64 {
	q := normalizeRecallText(query)
	t := normalizeRecallText(candidate)
	if q == "" || t == "" {
		return 0
	}
	if strings.Contains(t, q) {
		return 1
	}
	qr := []rune(q)
	if len(qr) == 1 {
		if strings.Contains(t, q) {
			return 1
		}
		return 0
	}
	grams := make(map[string]struct{}, len(qr)-1)
	for i := 0; i+1 < len(qr); i++ {
		grams[string(qr[i:i+2])] = struct{}{}
	}
	matched := 0
	for gram := range grams {
		if strings.Contains(t, gram) {
			matched++
		}
	}
	return float64(matched) / float64(len(grams))
}

func normalizeRecallText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, stop := range []string{
		"你还记得", "我记得", "前几天", "前些天", "上次", "之前", "以前", "当时",
		"我说过", "我说", "说过", "提到", "哪个", "什么", "是什么", "帮我", "一下",
		"今天", "昨天", "明天", "时候", "东西是", "吗", "呢",
	} {
		text = strings.ReplaceAll(text, stop, "")
	}
	var b strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
