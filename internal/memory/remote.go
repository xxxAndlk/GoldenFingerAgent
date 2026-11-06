package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"goldenfinger/agent/internal/store"
)

// RemoteConfig 装配云端共享长期记忆客户端。
type RemoteConfig struct {
	BaseURL  string
	Token    string
	DeviceID string
	// OwnerID 仅用于本地 Outbox 统计；云端 owner 由设备 Token 解析。
	OwnerID string
	DB      *store.DB
	HTTP    *http.Client
	Now     func() time.Time
	Logger  func(format string, args ...any)

	PollInterval time.Duration
}

func (c RemoteConfig) withDefaults() RemoteConfig {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = log.Printf
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 30 * time.Second
	}
	return c
}

// Remote 是云端共享长期记忆的端侧实现：写入先落本地持久 Outbox，
// 后台按退避策略上传；检索实时查询云端，失败由上层降级。
type Remote struct {
	cfg    RemoteConfig
	outbox *store.MemoryOutboxRepo

	notify chan struct{}
	once   sync.Once

	flushMu sync.Mutex
	mu      sync.Mutex
	paused  bool
	lastOK  time.Time
	lastErr string
	// clockOffset 是本机时钟与云端时钟的差值：两端设备时间不准时，
	// 槽位的新旧判断才不会判反。
	clockOffset time.Duration
}

// adjustedTime 把本机时间换算成云端时间轴上的时刻。
func (r *Remote) adjustedTime(at time.Time) time.Time {
	r.mu.Lock()
	offset := r.clockOffset
	r.mu.Unlock()
	return at.Add(offset)
}

// noteServerTime 记录云端时间与本机时间的差值（最多采纳 10 分钟的偏差）。
func (r *Remote) noteServerTime(serverTimeMs int64) {
	if serverTimeMs <= 0 {
		return
	}
	offset := time.UnixMilli(serverTimeMs).Sub(r.cfg.Now())
	if offset > 10*time.Minute {
		offset = 10 * time.Minute
	}
	if offset < -10*time.Minute {
		offset = -10 * time.Minute
	}
	r.mu.Lock()
	r.clockOffset = offset
	r.mu.Unlock()
}

// NewRemote 创建云端记忆客户端。
func NewRemote(cfg RemoteConfig) (*Remote, error) {
	cfg = cfg.withDefaults()
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("memory: 云端记忆需要 base_url 与 token")
	}
	if cfg.DB == nil {
		return nil, errors.New("memory: 云端记忆需要本地数据库保存 Outbox")
	}
	return &Remote{
		cfg:    cfg,
		outbox: store.NewRepos(cfg.DB).MemoryOutbox,
		notify: make(chan struct{}, 1),
	}, nil
}

type remoteEpisodePayload struct {
	SourceID      string `json:"source_id"`
	Text          string `json:"text"`
	Source        string `json:"source"`
	Role          string `json:"role"`
	ReferenceTime string `json:"reference_time"`
	ContentHash   string `json:"content_hash"`
	// Slot 是共享内容的位置：云端按槽位原子替换旧版本。
	Slot string `json:"slot,omitempty"`
	// SlotUpdatedAt 是写入时间（毫秒）：云端据此判断新旧，
	// 离线很久的旧请求恢复后不能覆盖更新的内容。
	SlotUpdatedAt int64 `json:"slot_updated_at,omitempty"`
}

// Append 先持久化到 Outbox 再异步上传；页面/聊天主流程不会因网络失败而阻塞。
func (r *Remote) Append(ctx context.Context, episode Episode) error {
	if strings.TrimSpace(episode.Text) == "" {
		return nil
	}
	at := episode.ReferenceTime
	if at.IsZero() {
		at = r.cfg.Now()
	}
	role := episode.Role
	if role == "" {
		role = RoleOwner
	}
	updatedAt := episode.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = r.cfg.Now()
	}
	payload, err := json.Marshal(remoteEpisodePayload{
		SourceID: episode.SourceID, Text: episode.Text, Source: episode.Source,
		Role: role, ReferenceTime: at.Format(time.RFC3339Nano),
		ContentHash: contentHashOf(episode.Text),
		Slot:        episode.Slot,
		// 用云端时间校正过的写入时刻：两端时钟有偏差时，
		// 「谁更新」才不会判错。
		SlotUpdatedAt: r.adjustedTime(updatedAt).UnixMilli(),
	})
	if err != nil {
		return err
	}
	if err := r.outbox.Enqueue(ctx, &store.MemoryOutboxEntry{
		OwnerUserID: episode.OwnerID, SourceID: episode.SourceID,
		PayloadJSON: string(payload), ContentHash: contentHashOf(episode.Text),
	}); err != nil {
		return err
	}
	r.wake()
	return nil
}

func contentHashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (r *Remote) wake() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// Start 运行后台上传循环直到 ctx 结束。
func (r *Remote) Start(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		r.Flush(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.notify:
		}
	}
}

// Flush 尝试上传一轮到期条目，返回成功上传数（测试与手动触发使用）。
func (r *Remote) Flush(ctx context.Context) int {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()

	entries, err := r.outbox.Due(ctx, r.cfg.Now(), 20)
	if err != nil {
		r.setError(fmt.Sprintf("读取 Outbox: %v", err))
		return 0
	}
	sent := 0
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		status, retryAfter, err := r.deliver(ctx, entry)
		now := r.cfg.Now()
		switch {
		case err == nil:
			if markErr := r.outbox.MarkSent(ctx, entry.OwnerUserID, entry.SourceID, now); markErr != nil {
				r.setError(markErr.Error())
				continue
			}
			sent++
			r.mu.Lock()
			r.lastOK = now
			r.lastErr = ""
			r.paused = false
			r.mu.Unlock()
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			_ = r.outbox.MarkPaused(ctx, entry.OwnerUserID, entry.SourceID, "需要重新授权 (HTTP "+strconv.Itoa(status)+")")
			r.mu.Lock()
			r.paused = true
			r.lastErr = "需要重新授权"
			r.mu.Unlock()
			return sent
		case status == http.StatusGone:
			_ = r.outbox.MarkFailed(ctx, entry.OwnerUserID, entry.SourceID, "该来源已被遗忘")
		case status == http.StatusBadRequest || status == http.StatusConflict ||
			status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity:
			// 数据异常：停止自动重试，等待人工处理。
			_ = r.outbox.MarkFailed(ctx, entry.OwnerUserID, entry.SourceID, err.Error())
		default:
			next := now.Add(retryDelay(entry.Attempts, retryAfter))
			_ = r.outbox.MarkRetry(ctx, entry.OwnerUserID, entry.SourceID, errorText(err), next)
			r.setError(errorText(err))
		}
	}
	return sent
}

// deliver 执行一条待发记录：写入片段，或执行删除指令。
func (r *Remote) deliver(ctx context.Context, entry store.MemoryOutboxEntry) (int, time.Duration, error) {
	if entry.Op == store.MemoryOutboxDelete {
		return r.deleteRemote(ctx, entry.PayloadJSON)
	}
	_, status, retryAfter, err := r.uploadEpisode(ctx, entry.PayloadJSON)
	return status, retryAfter, err
}

// RetryFailed 把失败条目重新排队（界面上「重试」按钮用）。
func (r *Remote) RetryFailed(ctx context.Context, ownerID string) (int64, error) {
	if ownerID == "" {
		return 0, errors.New("memory: 重试失败条目需要 owner")
	}
	return r.outbox.RetryFailed(ctx, ownerID)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

func retryDelay(attempts int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > 10*time.Minute {
			return 10 * time.Minute
		}
		return retryAfter
	}
	shift := attempts
	if shift > 7 {
		shift = 7
	}
	delay := 5 * time.Second << shift
	if delay > 10*time.Minute {
		delay = 10 * time.Minute
	}
	return delay
}

// httpError 携带状态码与重试提示，供 Outbox 决定重试策略。
type httpError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *httpError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	if body == "" {
		return fmt.Sprintf("memory: http %d", e.Status)
	}
	return fmt.Sprintf("memory: http %d: %s", e.Status, body)
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

func (r *Remote) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.cfg.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return r.cfg.HTTP.Do(req)
}

func (r *Remote) uploadEpisode(ctx context.Context, payload string) (string, int, time.Duration, error) {
	resp, err := r.do(ctx, http.MethodPost, "/api/memory/v1/episodes", strings.NewReader(payload))
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")),
			&httpError{Status: resp.StatusCode, Body: string(raw)}
	}
	var decoded struct {
		Status     string `json:"status"`
		ServerTime int64  `json:"server_time"`
	}
	_ = json.Unmarshal(raw, &decoded)
	r.noteServerTime(decoded.ServerTime)
	return decoded.Status, resp.StatusCode, 0, nil
}

// forgetRemotePayload 是排队中的删除指令。
type forgetRemotePayload struct {
	SourceID     string `json:"source_id,omitempty"`
	SourcePrefix string `json:"source_prefix,omitempty"`
	FactID       string `json:"fact_id,omitempty"`
}

// deleteRemote 执行一条排队中的删除指令。
func (r *Remote) deleteRemote(ctx context.Context, payload string) (int, time.Duration, error) {
	resp, err := r.do(ctx, http.MethodPost, "/api/memory/v1/forget", strings.NewReader(payload))
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode == http.StatusGone {
		// 已经是遗忘墓碑：视为完成。
		return resp.StatusCode, 0, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")),
			&httpError{Status: resp.StatusCode, Body: string(raw)}
	}
	return resp.StatusCode, 0, nil
}

// Search 实时查询云端；错误原样返回，让调用方明确降级而不是伪造成“没有记忆”。
func (r *Remote) Search(ctx context.Context, query MemoryQuery) ([]MemoryFact, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 8
	}
	raw, err := json.Marshal(map[string]any{"query": query.Query, "limit": limit})
	if err != nil {
		return nil, err
	}
	resp, err := r.do(ctx, http.MethodPost, "/api/memory/v1/search", bytes.NewReader(raw))
	if err != nil {
		r.setError(err.Error())
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{Status: resp.StatusCode, Body: string(body)}
	}
	var decoded struct {
		Facts []struct {
			ID         string   `json:"id"`
			Text       string   `json:"text"`
			ValidAt    string   `json:"valid_at"`
			InvalidAt  string   `json:"invalid_at"`
			SourceRefs []string `json:"source_refs"`
			Status     string   `json:"status"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	out := make([]MemoryFact, 0, len(decoded.Facts))
	for _, item := range decoded.Facts {
		fact := MemoryFact{ID: item.ID, Text: item.Text, SourceRefs: item.SourceRefs, Status: item.Status}
		if at, err := time.Parse(time.RFC3339Nano, item.ValidAt); err == nil {
			fact.ValidAt = &at
		}
		if at, err := time.Parse(time.RFC3339Nano, item.InvalidAt); err == nil {
			fact.InvalidAt = &at
		}
		out = append(out, fact)
	}
	r.mu.Lock()
	r.lastOK = r.cfg.Now()
	r.lastErr = ""
	r.mu.Unlock()
	return out, nil
}

// Forget 调用云端删除，并清理本机相同 SourceID 的待发事件。
func (r *Remote) Forget(ctx context.Context, request ForgetRequest) error {
	payload := forgetRemotePayload{}
	key := ""
	switch {
	case request.FactID != "":
		payload.FactID = request.FactID
		key = "forget:fact:" + request.FactID
	case request.SourcePrefix != "":
		payload.SourcePrefix = request.SourcePrefix
		key = "forget:" + request.SourcePrefix
	case request.SourceID != "":
		payload.SourceID = request.SourceID
		key = "forget:" + request.SourceID
	default:
		return errors.New("memory: 遗忘需要 source_id、source_prefix 或 fact_id")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := r.outbox.EnqueueDelete(ctx, &store.MemoryOutboxEntry{
		OwnerUserID: request.OwnerID, SourceID: key,
		PayloadJSON: string(raw), ContentHash: contentHashOf(string(raw)),
	}); err != nil {
		return err
	}
	// 清掉本机同来源的待发与已发记录：删除已经排队，
	// 之后再写同样的内容要能重新排队（来源 ID 按内容寻址，会重复）。
	if request.SourceID != "" {
		if err := r.outbox.PurgeBySource(ctx, request.OwnerID, request.SourceID); err != nil {
			return err
		}
	}
	if request.SourcePrefix != "" {
		if err := r.outbox.PurgeBySourcePrefix(ctx, request.OwnerID, request.SourcePrefix); err != nil {
			return err
		}
	}
	r.wake()
	return nil
}

// Health 报告云端可达性与本机待发/失败数量。
func (r *Remote) Health(ctx context.Context) (MemoryHealth, error) {
	health := MemoryHealth{Backend: "cloud-remote"}
	owner := r.cfg.OwnerID
	if owner != "" {
		if pending, failed, paused, err := r.outbox.Counts(ctx, owner); err == nil {
			health.Detail = fmt.Sprintf("待发=%d 失败=%d 暂停=%d；", pending, failed, paused)
		}
	}
	resp, err := r.do(ctx, http.MethodGet, "/api/memory/v1/health", nil)
	if err != nil {
		r.setError(err.Error())
		health.Ready = false
		health.Detail += "云端不可用：" + errorText(err)
		return health, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		r.setError(fmt.Sprintf("health http %d", resp.StatusCode))
		health.Ready = false
		health.Detail += fmt.Sprintf("云端健康检查失败 (HTTP %d)", resp.StatusCode)
		return health, nil
	}
	var decoded struct {
		Ready   bool   `json:"ready"`
		Backend string `json:"backend"`
		Pending int    `json:"pending"`
		Failed  int    `json:"failed"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return health, err
	}
	health.Ready = decoded.Ready
	if decoded.Backend != "" {
		health.Backend = decoded.Backend
	}
	if decoded.Detail != "" {
		health.Detail += "云端：" + decoded.Detail
	}
	r.mu.Lock()
	if r.lastOK.IsZero() {
		r.lastOK = r.cfg.Now()
	}
	r.mu.Unlock()
	return health, nil
}

// Resume 在重新授权后恢复暂停的 Outbox 条目。
func (r *Remote) Resume(ctx context.Context) (int64, error) {
	r.mu.Lock()
	r.paused = false
	r.mu.Unlock()
	return r.outbox.ResumePaused(ctx, r.cfg.OwnerID, r.cfg.Now())
}

// Paused 报告是否因鉴权失败暂停上传。
func (r *Remote) Paused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

func (r *Remote) setError(msg string) {
	r.mu.Lock()
	r.lastErr = strings.Join(strings.Fields(msg), " ")
	r.mu.Unlock()
}

// ProbeRemote 在不写本地 Outbox 的情况下检查云端可达性与鉴权。
// 设置页“测试连接”使用。
func ProbeRemote(ctx context.Context, baseURL, token string, timeout time.Duration) (MemoryHealth, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if base == "" || token == "" {
		return MemoryHealth{}, errors.New("memory: base_url 和 token 必填")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/memory/v1/health", nil)
	if err != nil {
		return MemoryHealth{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return MemoryHealth{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		return MemoryHealth{}, &httpError{Status: resp.StatusCode, Body: string(raw)}
	}
	var decoded struct {
		Ready   bool   `json:"ready"`
		Backend string `json:"backend"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return MemoryHealth{}, err
	}
	return MemoryHealth{Ready: decoded.Ready, Backend: decoded.Backend, Detail: decoded.Detail}, nil
}
