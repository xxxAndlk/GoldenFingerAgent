package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Handler 执行一条来自手机的指令。返回的 payload 会作为 result 回传手机。
// 实现必须是幂等的：同一 command id 可能因重连被重投。
type Handler func(ctx context.Context, payload json.RawMessage) (ResultPayload, error)

// Client 是电脑端的中继客户端：注册、配对码、保持在线、执行并回传结果。
type Client struct {
	// BaseURL 是中继 HTTP 地址，例如 https://relay.example.com。
	BaseURL string
	// DesktopID / Token 由 Register 获得并应持久化在本机。
	DesktopID string
	Token     string
	// Name 展示给手机端。
	Name string
	// Handle 执行指令；必填。
	Handle Handler
	// Enrich 在回传结果前补充交付物引用（可选）。
	Enrich func(ctx context.Context, payload *ResultPayload)
	// Ledger 持久化已执行指令的回执（可选）。
	// 配了它，「最多执行一次」在电脑重启后依然成立：
	// 已完成的重投返回原结果，中断过的按 unknown 回执。
	Ledger CommandLedger
	// CommandTimeout 单条指令的执行上限。
	CommandTimeout time.Duration
	// Logger 默认 log.Printf。
	Logger func(format string, args ...any)

	httpc *http.Client
	mu    sync.Mutex
	// results 是本进程内已完成指令的结果，供重投时原样回放。
	results map[string]ResultPayload
	// order 记录 results 的写入顺序，用于淘汰旧条目。
	order []string
	// inflight 是正在执行的指令：同一条指令并发重投时等第一次的结果，
	// 不再执行第二遍。
	inflight map[string]*commandCall
	seenN    int
	closed   chan struct{}
	once     sync.Once
	// connected 标记当前是否与中继保持连接（受 mu 保护）。
	connected bool
}

// commandCall 是一次正在执行的指令。
type commandCall struct {
	done   chan struct{}
	result ResultPayload
}

// ErrNoHandler 表示未配置执行器。
var ErrNoHandler = errors.New("relay: handler is required")

func (c *Client) init() {
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = 30 * time.Minute
	}
	if c.Logger == nil {
		c.Logger = log.Printf
	}
	if c.httpc == nil {
		c.httpc = &http.Client{Timeout: 20 * time.Second}
	}
	if c.results == nil {
		c.results = make(map[string]ResultPayload)
		c.inflight = make(map[string]*commandCall)
		c.seenN = 4096
	}
	if c.closed == nil {
		c.closed = make(chan struct{})
	}
}

func (c *Client) base() string { return strings.TrimRight(c.BaseURL, "/") }

// Register 在中继上登记这台电脑，返回应持久化的 id 与 token。
func (c *Client) Register(ctx context.Context) (string, string, error) {
	c.init()
	body, _ := json.Marshal(map[string]string{"name": c.Name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/v1/desktop/register", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("relay register: %s", resp.Status)
	}
	var out struct {
		DesktopID string `json:"desktop_id"`
		Token     string `json:"desktop_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	return out.DesktopID, out.Token, nil
}

// PairCode 获取一次性配对码，供用户在手机上输入。
func (c *Client) PairCode(ctx context.Context) (code string, expiresAt time.Time, err error) {
	c.init()
	if c.DesktopID == "" || c.Token == "" {
		return "", time.Time{}, ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/v1/pair/code", nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("X-Desktop-Id", c.DesktopID)
	req.Header.Set("X-Desktop-Token", c.Token)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("relay pair code: %s", resp.Status)
	}
	var out struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, err
	}
	expiresAt, _ = time.Parse(time.RFC3339, out.ExpiresAt)
	return out.Code, expiresAt, nil
}

// Channels 列出已配对手机。
func (c *Client) Channels(ctx context.Context) ([]map[string]string, error) {
	c.init()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/v1/channels", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Desktop-Id", c.DesktopID)
	req.Header.Set("X-Desktop-Token", c.Token)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay channels: %s", resp.Status)
	}
	var out struct {
		Channels []map[string]string `json:"channels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Channels, nil
}

// Close 停止 Run 循环。
func (c *Client) Close() {
	c.once.Do(func() {
		if c.closed != nil {
			close(c.closed)
		}
	})
}

// Run 保持与中继的长连接直到 ctx 结束，断线自动重连（指数退避上限 30s）。
func (c *Client) Run(ctx context.Context) error {
	c.init()
	if c.Handle == nil {
		return ErrNoHandler
	}
	if c.DesktopID == "" || c.Token == "" {
		return ErrUnauthorized
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.Logger("[relay] session ended: %v; retry in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closed:
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return ctx.Err()
}

func (c *Client) wsURL() string {
	u := c.base()
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	q := url.Values{}
	q.Set("role", RoleDesktop)
	q.Set("id", c.DesktopID)
	q.Set("pv", strconv.Itoa(ProtocolVersionV2))
	return u + "/v1/ws?" + q.Encode()
}

func (c *Client) session(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	header := http.Header{}
	header.Set("X-Token", c.Token)
	ws, _, err := dialer.DialContext(ctx, c.wsURL(), header)
	if err != nil {
		return err
	}
	defer ws.Close()
	c.setConnected(true)
	defer c.setConnected(false)
	c.Logger("[relay] connected(%s)", c.DesktopID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var env Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			switch env.Kind {
			case KindHello:
				c.Logger("[relay] hello channel=%s", env.Channel)
			case KindPing:
				c.mu.Lock()
				_ = ws.WriteJSON(&Envelope{V: ProtocolVersion, ID: env.ID, Kind: KindPong, TS: time.Now().UnixMilli()})
				c.mu.Unlock()
			case KindCommand:
				go c.execute(ctx, ws, env)
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return nil
	case <-done:
		return errors.New("relay: connection closed")
	}
}

// execute 执行一条指令并回传 result。同一幂等键最多执行一次。
//
// 三种重投都在这里处理：
//   - 已经完成 → 原样回放上次的结果；
//   - 本进程里还在执行 → 等第一次的结果，不再执行第二遍；
//   - 持久化回执里只剩 running → 上次执行被中断，按 unknown 回执。
func (c *Client) execute(ctx context.Context, ws *websocket.Conn, env Envelope) {
	key := commandKey(env)
	call, owner := c.beginCommand(key)
	if !owner {
		<-call.done
		c.sendResult(ws, env, call.result)
		return
	}
	previous, found, fatal := c.cachedResult(ctx, key)
	if found {
		result := previous
		if fatal {
			// 回执读不出来：不能重跑，也不要把它记成「已完成」，
			// 否则一次数据库故障会把这个幂等键永久钉死。
			c.abortCommand(key, result)
		} else {
			c.finishCommand(key, result)
		}
		c.sendResult(ws, env, result)
		return
	}
	if c.interruptedReceipt(ctx, key) {
		result := ResultPayload{
			OK: false, Command: CommandUnknown,
			Error: "这条指令上一次执行中断，结果未知，请先核对再重试",
		}
		c.finishCommand(key, result)
		c.sendResult(ws, env, result)
		return
	}
	// 执行前必须先把「已开始」写进回执。写不进去就不能执行业务：
	// 否则进程重启后电脑无法识别这次执行，「最多一次」就不再成立。
	if err := c.markRunning(ctx, key); err != nil {
		c.Logger("[relay] ledger begin %s failed, refusing to execute: %v", key, err)
		result := ResultPayload{
			OK: false, Command: CommandUnknown,
			Error: "电脑本地回执写入失败，无法保证只执行一次，请核对后再试",
		}
		c.abortCommand(key, result)
		c.sendResult(ws, env, result)
		return
	}

	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, c.CommandTimeout)
	defer cancel()
	result, err := c.Handle(runCtx, env.Payload)
	result.Elapsed = time.Since(started).Milliseconds()
	if err != nil {
		result.OK = false
		if result.Error == "" {
			result.Error = err.Error()
		}
	}
	if c.Enrich != nil {
		c.Enrich(runCtx, &result)
	}
	c.recordComplete(ctx, key, result)
	c.finishCommand(key, result)
	c.sendResult(ws, env, result)
}

// beginCommand 占用一条指令的执行权。
//
// 返回 owner=true 表示这次调用负责执行；owner=false 表示已有同一条指令
// 在执行，调用方应等待 call.done 然后用 call.result 回执。
func (c *Client) beginCommand(key string) (*commandCall, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if call, ok := c.inflight[key]; ok {
		return call, false
	}
	call := &commandCall{done: make(chan struct{})}
	c.inflight[key] = call
	return call, true
}

// finishCommand 记录结果并唤醒等待同一条指令的其他投递。
func (c *Client) finishCommand(key string, result ResultPayload) {
	c.completeCommand(key, result, true)
}

// abortCommand 释放执行权但不缓存结果。
//
// 用于「回执本身不可用」的失败路径：这次没有真正执行，
// 后续重投应当再次尝试，而不是被一条假的 unknown 永久挡住。
func (c *Client) abortCommand(key string, result ResultPayload) {
	c.completeCommand(key, result, false)
}

func (c *Client) completeCommand(key string, result ResultPayload, remember bool) {
	c.mu.Lock()
	call, ok := c.inflight[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	call.result = result
	delete(c.inflight, key)
	if remember {
		c.rememberLocked(key, result)
	}
	close(call.done)
	c.mu.Unlock()
}

// rememberLocked 记录完成结果，并把最旧的条目淘汰掉。
func (c *Client) rememberLocked(key string, result ResultPayload) {
	c.results[key] = result
	c.order = append(c.order, key)
	for len(c.order) > c.seenN {
		old := c.order[0]
		c.order = c.order[1:]
		delete(c.results, old)
	}
}

// cachedResult 查已经完成的回执：先看本进程缓存，再看持久化回执。
//
// 第二个返回值表示「找到了一个要直接回执的结果」；第三个返回值表示
// 这个结果来自回执读取失败（fatal），调用方不能把它缓存成已完成。
func (c *Client) cachedResult(ctx context.Context, key string) (ResultPayload, bool, bool) {
	c.mu.Lock()
	cached, ok := c.results[key]
	c.mu.Unlock()
	if ok {
		return cached, true, false
	}
	if c.Ledger == nil {
		return ResultPayload{}, false, false
	}
	status, resultJSON, found, err := c.Ledger.Lookup(ctx, key)
	if err != nil {
		// 回执读不出来就不能盲目重跑：宁可让用户核对。
		c.Logger("[relay] ledger lookup %s: %v", key, err)
		return ResultPayload{
			OK: false, Command: CommandUnknown,
			Error: "无法确认这条指令上一次是否执行过，请先核对再重试",
		}, true, true
	}
	if !found || status != CommandReceiptDone {
		return ResultPayload{}, false, false
	}
	var previous ResultPayload
	if len(resultJSON) > 0 && json.Unmarshal(resultJSON, &previous) == nil {
		c.Logger("[relay] duplicate command %s answered from receipt", key)
		return previous, true, false
	}
	return ResultPayload{}, false, false
}

// interruptedReceipt 判断持久化回执里是不是只剩一条「开始过但没有结果」的记录。
// 有这种记录说明上一次执行被中断，结果未知。
func (c *Client) interruptedReceipt(ctx context.Context, key string) bool {
	if c.Ledger == nil {
		return false
	}
	status, _, found, err := c.Ledger.Lookup(ctx, key)
	if err != nil {
		return false
	}
	return found && status != CommandReceiptDone
}

// markRunning 在真正执行前落一条「已开始」记录。
//
// 返回错误时调用方必须放弃执行：没有 running 回执的执行，
// 在电脑重启后无法被识别，也就破坏了「最多执行一次」。
func (c *Client) markRunning(ctx context.Context, key string) error {
	if c.Ledger == nil {
		return nil
	}
	return c.Ledger.Begin(ctx, key, time.Now())
}

// recordComplete 保存这次执行的结果，供重投时原样回给手机。
func (c *Client) recordComplete(ctx context.Context, key string, result ResultPayload) {
	if c.Ledger == nil {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	if err := c.Ledger.Complete(ctx, key, raw, time.Now()); err != nil {
		c.Logger("[relay] ledger complete %s: %v", key, err)
	}
}

// sendResult 把执行结果回传给手机。
func (c *Client) sendResult(ws *websocket.Conn, env Envelope, result ResultPayload) {
	reply := &Envelope{
		V: ProtocolVersion, ID: newMessageID(), Kind: KindResult,
		TS: time.Now().UnixMilli(), ReplyTo: env.ID, Channel: env.Channel,
		Payload: mustJSON(result),
	}
	payload, _ := json.Marshal(reply)
	c.mu.Lock()
	err := ws.WriteMessage(websocket.TextMessage, payload)
	c.mu.Unlock()
	if err != nil {
		c.Logger("[relay] write result %s: %v", env.ID, err)
	}
}

// commandKey 取指令的幂等键：优先用载荷里的 idempotency_key，
// 没有时退回信封 ID。
//
// 手机端重投时信封 ID 会换新的（否则中继会当成重复丢掉），
// 幂等键保持不变，电脑靠它认出「这是同一条指令」。
func commandKey(env Envelope) string {
	var probe struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	if json.Unmarshal(env.Payload, &probe) == nil {
		if key := strings.TrimSpace(probe.IdempotencyKey); key != "" {
			return key
		}
	}
	return env.ID
}

// ExecuteLocal 是给服务端用的便捷执行器：把手机指令转发给本机 HTTP 接口。
func ExecuteLocal(httpBase string, client *http.Client) Handler {
	if client == nil {
		client = &http.Client{}
	}
	base := strings.TrimRight(httpBase, "/")
	return func(ctx context.Context, payload json.RawMessage) (ResultPayload, error) {
		var p localRequest
		if err := json.Unmarshal(payload, &p); err != nil {
			return ResultPayload{}, fmt.Errorf("relay: bad payload: %w", err)
		}
		switch p.Kind {
		case "job":
			return submitJob(ctx, client, base, p)
		case "job_status":
			return getJob(ctx, client, base, p.TaskID)
		case "job_cancel":
			return cancelJob(ctx, client, base, p.TaskID)
		}
		if result, handled := forwardCommand(ctx, client, base, p); handled {
			return result, nil
		}
		path := "/api/chat"
		body := map[string]any{"text": stringOr(p.Text, p.Kind)}
		if p.Agent != "" || p.Dir != "" {
			path = "/api/agents/delegate"
			body = map[string]any{"agent_id": p.Agent, "task": stringOr(p.Text, p.Kind), "work_dir": p.Dir}
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
		if err != nil {
			return ResultPayload{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return ResultPayload{}, err
		}
		defer resp.Body.Close()
		var decoded struct {
			Reply  string `json:"reply"`
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&decoded)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := decoded.Error
			if msg == "" {
				msg = resp.Status
			}
			return ResultPayload{OK: false, Error: msg}, nil
		}
		return ResultPayload{OK: true, Reply: stringOr(decoded.Reply, decoded.Output)}, nil
	}
}

// localRequest 是手机经中继发来的指令（chat / job / job_status / job_cancel
// 以及白名单里的长期任务控制面命令）。
type localRequest struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	Agent string `json:"agent_id"`
	Dir   string `json:"work_dir"`
	// TaskID 用于 job_status / job_cancel 以及 work_task_* 系列。
	TaskID string `json:"task_id"`
	// IdempotencyKey 让手机端重试同一条指令时只落一个任务。
	IdempotencyKey string `json:"idempotency_key"`
	// SchemaVersion 是手机端声明的响应结构版本。
	SchemaVersion int `json:"schema_version"`
	// ConversationKind 是 conversation_list 的类别过滤（main/worker/delegated）。
	ConversationKind string `json:"conversation_kind"`
	// Action 是 work_task_action 的动作名，也用于 collection_configure 的
	// add/remove/enable/disable。
	Action string `json:"action"`
	// Status 是 work_task_list 的状态过滤。
	Status string `json:"status"`
	// SessionID 是 conversation_messages 的会话 ID。
	SessionID string `json:"session_id"`
	// DeviceID 是发起方的设备标识：已读游标按设备分别记录。
	DeviceID string `json:"device_id"`
	// Before 是 conversation_messages 的向上分页游标。
	Before string `json:"before"`
	// Limit 是列表与消息分页的条数上限。
	Limit int `json:"limit"`
	// OutboxID 是审批动作的 outbox ID。
	OutboxID string `json:"outbox_id"`
	// ArtifactID 是交付物 ID。
	ArtifactID string `json:"artifact_id"`
	// BriefID 是简报 ID。
	BriefID string `json:"brief_id"`
	// Date 是简报日期（YYYY-MM-DD）。
	Date string `json:"date"`
	// Version 是交付物版本号。
	Version int `json:"version"`
	// ExpectedVersion 是乐观锁版本；过期时电脑返回冲突。
	ExpectedVersion *int `json:"expected_version"`
	// Reason 是驳回或要求修改时给的理由。
	Reason string `json:"reason"`
	// Title/Goal/Profile 是新建任务的参数。
	Title   string `json:"title"`
	Goal    string `json:"goal"`
	Profile string `json:"profile"`
	// Channel 与 Enabled 是桌面采集渠道配置。
	Channel string `json:"channel"`
	Enabled bool   `json:"enabled"`
	// Query 是微信 add 的联系人或群名称。
	Query string `json:"query"`
	// SourceID 是微信候选的稳定 ID。
	SourceID string `json:"source_id"`
	// URL 是网页采集地址。
	URL string `json:"url"`
	// SiteID 是已有网页配置的 ID。
	SiteID string `json:"site_id"`
	// Scope 是网页采集范围：current_page / site。
	Scope string `json:"scope"`
	// IntervalSeconds 是网页采集间隔。
	IntervalSeconds int `json:"interval_seconds"`
	// Config 是渠道配置的其余字段，原样透传给桌面。
	Config map[string]any `json:"config"`
}

// localJob 是电脑端 /api/jobs 的响应形状（只取中继需要的字段）。
type localJob struct {
	Task struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Output      string `json:"output"`
		Error       string `json:"error"`
		Task        string `json:"task"`
		WorkTaskID  string `json:"work_task_id"`
		SessionID   string `json:"session_id"`
		ResultBrief string `json:"result_summary"`
	} `json:"task"`
	Events []struct {
		Type string `json:"type"`
		Body string `json:"body"`
	} `json:"events"`
	Error string `json:"error"`
}

func doLocal(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, localJob, error) {
	var out localJob
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, out, err
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := stringOr(out.Error, out.Task.Error)
		if msg == "" {
			msg = resp.Status
		}
		return nil, out, fmt.Errorf("relay: %s", msg)
	}
	return resp, out, nil
}

func submitJob(ctx context.Context, client *http.Client, base string, p localRequest) (ResultPayload, error) {
	raw, _ := json.Marshal(map[string]any{
		"agent_id": p.Agent, "task": p.Text, "work_dir": p.Dir, "idempotency_key": p.IdempotencyKey,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/jobs", bytes.NewReader(raw))
	if err != nil {
		return ResultPayload{}, err
	}
	_, out, err := doLocal(ctx, client, req)
	if err != nil {
		return ResultPayload{OK: false, Error: err.Error()}, nil
	}
	return ResultPayload{
		OK: true, TaskID: out.Task.ID, JobID: out.Task.ID, WorkTaskID: out.Task.WorkTaskID,
		Status: out.Task.Status,
		Reply:  "已交给电脑后台执行（任务 " + shortID(out.Task.ID) + "）",
	}, nil
}

func getJob(ctx context.Context, client *http.Client, base, taskID string) (ResultPayload, error) {
	if taskID == "" {
		return ResultPayload{OK: false, Error: "缺少 task_id"}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/jobs/"+url.PathEscape(taskID), nil)
	if err != nil {
		return ResultPayload{}, err
	}
	_, out, err := doLocal(ctx, client, req)
	if err != nil {
		return ResultPayload{OK: false, Error: err.Error()}, nil
	}
	return jobToResult(out), nil
}

func cancelJob(ctx context.Context, client *http.Client, base, taskID string) (ResultPayload, error) {
	if taskID == "" {
		return ResultPayload{OK: false, Error: "缺少 task_id"}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/api/jobs/"+url.PathEscape(taskID), nil)
	if err != nil {
		return ResultPayload{}, err
	}
	_, out, err := doLocal(ctx, client, req)
	if err != nil {
		return ResultPayload{OK: false, Error: err.Error()}, nil
	}
	res := jobToResult(out)
	res.Reply = "已请求取消任务 " + shortID(out.Task.ID)
	return res, nil
}

// jobToResult 把任务快照转成手机能直接显示的结论 + 进度事件。
//
// AgentJob 是执行段；它可能挂在某个长期 WorkTask 下。
// 返回里同时带 job_id 与 work_task_id，手机据此把两种控制面映射起来。
func jobToResult(job localJob) ResultPayload {
	if len(job.Events) == 0 && job.Task.ID == "" {
		return ResultPayload{OK: false, Error: "任务不存在"}
	}
	events := make([]string, 0, len(job.Events))
	for _, e := range job.Events {
		events = append(events, e.Type+": "+e.Body)
	}
	reply := job.Task.Output
	if reply == "" {
		reply = job.Task.ResultBrief
	}
	if reply == "" {
		reply = job.Task.Error
	}
	return ResultPayload{
		OK: job.Task.Error == "", Reply: reply, Error: job.Task.Error,
		TaskID: job.Task.ID, JobID: job.Task.ID, WorkTaskID: job.Task.WorkTaskID,
		Status: job.Task.Status, Events: events,
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// RevokeChannel 解除通道绑定。
func (c *Client) RevokeChannel(ctx context.Context, channelID string) error {
	c.init()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base()+"/v1/channels/"+url.PathEscape(channelID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Desktop-Id", c.DesktopID)
	req.Header.Set("X-Desktop-Token", c.Token)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("relay revoke: %s", resp.Status)
	}
	return nil
}

func stringOr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// setConnected 更新在线标记。
func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	c.connected = v
	c.mu.Unlock()
}

// newMessageID 生成客户端侧消息 id（幂等键）。
func newMessageID() string {
	token, err := randomToken(8)
	if err != nil {
		return fmt.Sprintf("m-%d", time.Now().UnixNano())
	}
	return "m-" + token
}
