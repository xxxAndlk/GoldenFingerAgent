package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
)

// NativeConfig 装配内置长期记忆。它复用现有 SQLite、模型与嵌入器，
// 不引入图数据库或外部进程，因此不增加打包体积与运行时依赖。
type NativeConfig struct {
	Repos *store.Repos
	LLM   llm.Client
	Model string
	Embed llm.Embedder
	Now   func() time.Time

	// PollInterval 是后台抽取的轮询间隔；Notify 会在新片段到达时立即唤醒。
	PollInterval time.Duration
	// Batch 是单轮最多处理的片段数。
	Batch int
	// MaxAttempts 是抽取失败后的最大重试次数，超过即标记 failed 并保留原文。
	MaxAttempts int
	// SearchLimit 是检索时载入的事实条数上限（家庭数据量级足够）。
	SearchLimit int
	// RecencyHalfLife 控制时效衰减。
	RecencyHalfLife time.Duration
	// Logger 默认 log.Printf。
	Logger func(format string, args ...any)
}

func (c NativeConfig) withDefaults() NativeConfig {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.Batch <= 0 {
		c.Batch = 5
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.SearchLimit <= 0 {
		c.SearchLimit = 800
	}
	if c.RecencyHalfLife <= 0 {
		c.RecencyHalfLife = 30 * 24 * time.Hour
	}
	if c.Logger == nil {
		c.Logger = log.Printf
	}
	return c
}

// Native 是纯 Go 的长期记忆后端：原始片段落 SQLite，后台用模型抽取事实，
// 检索时融合向量、关键词与时效，并按主体做一跳扩展。
type Native struct {
	cfg NativeConfig

	notify chan struct{}
	once   sync.Once

	mu        sync.Mutex
	lastError string
	processed int
	failed    int
}

// NewNative 创建内置长期记忆后端。
func NewNative(cfg NativeConfig) (*Native, error) {
	cfg = cfg.withDefaults()
	if cfg.Repos == nil || cfg.Repos.LongTerm == nil {
		return nil, errors.New("memory: 内置长期记忆需要 LongTerm 仓储")
	}
	return &Native{cfg: cfg, notify: make(chan struct{}, 1)}, nil
}

// Append 幂等保存片段并唤醒后台抽取。SQLite 原始证据先落地，
// 即使模型不可用也不影响聊天与既有记忆功能。
func (n *Native) Append(ctx context.Context, episode Episode) error {
	if strings.TrimSpace(episode.Text) == "" {
		return nil
	}
	at := episode.ReferenceTime
	if at.IsZero() {
		at = n.cfg.Now()
	}
	var embedding []float32
	if n.cfg.Embed != nil {
		if vecs, err := n.cfg.Embed.Embed(ctx, []string{episode.Text}); err == nil && len(vecs) == 1 {
			embedding = vecs[0]
		}
	}
	inserted, err := n.cfg.Repos.LongTerm.InsertEpisode(ctx, &store.LongTermEpisode{
		SourceID: episode.SourceID, OwnerUserID: episode.OwnerID, Text: episode.Text,
		Source: episode.Source, ReferenceTime: at, Embedding: embedding,
	})
	if err != nil {
		return err
	}
	if !inserted {
		return nil // 幂等：同一来源只抽取一次
	}
	n.wake()
	return nil
}

func (n *Native) wake() {
	select {
	case n.notify <- struct{}{}:
	default:
	}
}

// Start 运行后台抽取循环直到 ctx 结束。
func (n *Native) Start(ctx context.Context) {
	ticker := time.NewTicker(n.cfg.PollInterval)
	defer ticker.Stop()
	for {
		n.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-n.notify:
		}
	}
}

// RunOnce 处理一轮待抽取片段，返回处理条数（测试与手动触发使用）。
func (n *Native) RunOnce(ctx context.Context) int {
	eps, err := n.cfg.Repos.LongTerm.PendingEpisodes(ctx, n.cfg.Batch)
	if err != nil {
		n.setError(fmt.Sprintf("读取待抽取片段: %v", err))
		return 0
	}
	handled := 0
	for _, ep := range eps {
		if ctx.Err() != nil {
			return handled
		}
		if err := n.processEpisode(ctx, ep); err != nil {
			n.setError(err.Error())
		}
		handled++
	}
	return handled
}

func (n *Native) processEpisode(ctx context.Context, ep store.LongTermEpisode) error {
	facts, err := ExtractFacts(ctx, n.cfg.LLM, n.cfg.Model, ep.Text, ep.ReferenceTime)
	if err != nil {
		attempts := ep.Attempts + 1
		status := store.LongTermStatusPending
		if attempts >= n.cfg.MaxAttempts {
			// 放弃自动重试，但保留原文：原文仍是证据，用户可以再问起。
			status = store.LongTermStatusFailed
		}
		if markErr := n.cfg.Repos.LongTerm.MarkEpisode(ctx, ep.SourceID, status, err.Error()); markErr != nil {
			return fmt.Errorf("标记片段失败: %w", markErr)
		}
		n.mu.Lock()
		n.failed++
		n.mu.Unlock()
		return fmt.Errorf("抽取 %s: %w", ep.SourceID, err)
	}

	vectors := n.embedFacts(ctx, facts)
	for i, f := range facts {
		var vec []float32
		if i < len(vectors) {
			vec = vectors[i]
		}
		if err := n.applyFact(ctx, ep, f, vec); err != nil {
			return err
		}
	}
	if err := n.cfg.Repos.LongTerm.MarkEpisode(ctx, ep.SourceID, store.LongTermStatusDone, ""); err != nil {
		return err
	}
	n.mu.Lock()
	n.processed++
	n.lastError = ""
	n.mu.Unlock()
	return nil
}

func (n *Native) embedFacts(ctx context.Context, facts []extractedFact) [][]float32 {
	if n.cfg.Embed == nil || len(facts) == 0 {
		return nil
	}
	texts := make([]string, 0, len(facts))
	for _, f := range facts {
		texts = append(texts, f.Fact)
	}
	vecs, err := n.cfg.Embed.Embed(ctx, texts)
	if err != nil || len(vecs) != len(facts) {
		// 嵌入失败不影响事实入库，只是失去向量召回。
		return nil
	}
	return vecs
}

// applyFact 写入一条抽取事实：同三元组去重；inferred 不得让 confirmed 失效，
// 与已确认事实冲突时保留两条证据并标记 conflicted，等待用户处理。
func (n *Native) applyFact(ctx context.Context, ep store.LongTermEpisode, f extractedFact, vec []float32) error {
	validFrom := ep.ReferenceTime
	if f.ValidAt != "" {
		if parsed, err := time.Parse(time.RFC3339, f.ValidAt); err == nil {
			validFrom = parsed
		}
	}
	active, err := n.cfg.Repos.LongTerm.ActiveEdgesByTriple(ctx, ep.OwnerUserID, f.Subject, f.Relation)
	if err != nil {
		return err
	}
	sameFact := false
	conflict := false
	for _, old := range active {
		if strings.EqualFold(old.Object, f.Object) {
			// 同一事实重复出现：只补充来源引用，不产生重复条目。
			sameFact = true
			if err := n.cfg.Repos.LongTerm.AddSourceRef(ctx, old.ID, ep.SourceID); err != nil {
				return err
			}
			continue
		}
		if old.Status == store.MemoryConfirmed {
			// 自动抽取的 inferred 结论不能推翻用户确认过的事实。
			conflict = true
			continue
		}
		if old.Status == store.MemoryConflicted {
			continue
		}
		// 同一关系出现新值：旧值在 validFrom 之后不再成立。
		if err := n.cfg.Repos.LongTerm.InvalidateEdge(ctx, old.ID, validFrom); err != nil {
			return err
		}
	}
	if sameFact {
		return nil
	}
	status := store.MemoryInferred
	if conflict {
		status = store.MemoryConflicted
	}
	return n.cfg.Repos.LongTerm.InsertEdge(ctx, &store.LongTermEdge{
		ID: uuid.NewString(), OwnerUserID: ep.OwnerUserID, Subject: f.Subject, Relation: f.Relation,
		Object: f.Object, Fact: f.Fact, ValidFrom: &validFrom, SourceRefs: []string{ep.SourceID},
		Embedding: vec, Status: status, ScopeType: "global", Confidence: 0.55,
	})
}

// Search 融合向量、关键词与时效，并做一跳主体扩展。
func (n *Native) Search(ctx context.Context, query MemoryQuery) ([]MemoryFact, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 8
	}
	edges, err := n.cfg.Repos.LongTerm.Edges(ctx, query.OwnerID, n.cfg.SearchLimit)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return nil, nil
	}
	var queryVec []float32
	if n.cfg.Embed != nil {
		if vecs, err := n.cfg.Embed.Embed(ctx, []string{query.Query}); err == nil && len(vecs) == 1 {
			queryVec = vecs[0]
		}
	}
	includeHistory := historicalQuery(query.Query)
	scored := n.score(query.Query, queryVec, edges, includeHistory)

	// 一跳扩展：把命中主体下的其他事实一并给出，模拟图检索的邻域上下文。
	subjects := make([]string, 0, 3)
	seen := map[string]bool{}
	for _, item := range scored {
		if len(subjects) >= 3 {
			break
		}
		if item.edge.Subject == "" || seen[item.edge.Subject] {
			continue
		}
		seen[item.edge.Subject] = true
		subjects = append(subjects, item.edge.Subject)
	}
	if len(subjects) > 0 {
		neighbours, err := n.cfg.Repos.LongTerm.EdgesBySubjects(ctx, query.OwnerID, subjects, 12)
		if err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, item := range scored {
			known[item.edge.ID] = true
		}
		for _, edge := range neighbours {
			if known[edge.ID] || (!includeHistory && edge.ValidTo != nil) {
				continue
			}
			// 邻域事实只作为上下文补充，得分低于直接命中的事实。
			lex := lexicalScore(query.Query, edge.Fact)
			scored = append(scored, scoredEdge{edge: edge, lexical: lex, score: 0.5*lex + 0.05})
		}
	}

	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	out := make([]MemoryFact, 0, limit)
	for _, item := range scored {
		if len(out) >= limit {
			break
		}
		out = append(out, MemoryFact{
			ID: item.edge.ID, Text: item.edge.Fact,
			ValidAt: item.edge.ValidFrom, InvalidAt: item.edge.ValidTo,
			SourceRefs: item.edge.SourceRefs, Status: item.edge.Status,
		})
	}
	return out, nil
}

type scoredEdge struct {
	edge    store.LongTermEdge
	vector  float64
	lexical float64
	score   float64
}

func (n *Native) score(query string, queryVec []float32, edges []store.LongTermEdge, includeHistory bool) []scoredEdge {
	now := n.cfg.Now()
	out := make([]scoredEdge, 0, len(edges))
	for _, edge := range edges {
		if !includeHistory && edge.ValidTo != nil {
			continue
		}
		lex := lexicalScore(query, edge.Fact)
		var vec float64
		if len(queryVec) > 0 && len(edge.Embedding) == len(queryVec) {
			vec = float64(cosineSimilarity(queryVec, edge.Embedding))
		}
		if lex < 0.12 && vec <= 0 {
			continue
		}
		at := time.Time{}
		if edge.ValidFrom != nil {
			at = *edge.ValidFrom
		} else {
			at = edge.CreatedAt
		}
		recency := recencyScore(now, at, n.cfg.RecencyHalfLife)
		var score float64
		if len(queryVec) > 0 || vec > 0 {
			score = 0.55*vec + 0.35*lex + 0.10*recency
		} else {
			score = 0.85*lex + 0.15*recency
		}
		out = append(out, scoredEdge{edge: edge, vector: vec, lexical: lex, score: score})
	}
	return out
}

// Forget 真正删除长期记忆：按来源片段或其派生事实。
// 与 Graphiti 时期不同，这里不再返回 ErrUnsupported。
func (n *Native) Forget(ctx context.Context, request ForgetRequest) error {
	if request.OwnerID == "" {
		return errors.New("memory: 遗忘需要 owner")
	}
	switch {
	case request.FactID != "":
		return n.cfg.Repos.LongTerm.DeleteEdge(ctx, request.OwnerID, request.FactID)
	case request.SourceID != "":
		if _, err := n.cfg.Repos.LongTerm.DeleteEdgesBySource(ctx, request.OwnerID, request.SourceID); err != nil {
			return err
		}
		return n.cfg.Repos.LongTerm.DeleteEpisode(ctx, request.SourceID)
	default:
		return errors.New("memory: 遗忘需要 source_id 或 fact_id")
	}
}

// Health 如实报告内置后端状态。
func (n *Native) Health(ctx context.Context) (MemoryHealth, error) {
	pending, done, failed, err := n.cfg.Repos.LongTerm.EpisodeCounts(ctx)
	if err != nil {
		return MemoryHealth{Backend: "sqlite-native"}, err
	}
	n.mu.Lock()
	lastErr := n.lastError
	n.mu.Unlock()
	return MemoryHealth{
		Ready:   true,
		Backend: "sqlite-native",
		Detail: fmt.Sprintf("片段 pending=%d done=%d failed=%d；本轮已抽取=%d 失败=%d；last_error=%s",
			pending, done, failed, n.processed, n.failed, lastErr),
	}, nil
}

func (n *Native) setError(msg string) {
	n.mu.Lock()
	n.lastError = msg
	n.mu.Unlock()
}

// cosineSimilarity 与 store 内的实现保持一致（同包不可见，故此处独立实现）。
func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
