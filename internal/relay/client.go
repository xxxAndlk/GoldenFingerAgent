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
	// CommandTimeout 单条指令的执行上限。
	CommandTimeout time.Duration
	// Logger 默认 log.Printf。
	Logger func(format string, args ...any)

	httpc  *http.Client
	mu     sync.Mutex
	seen   map[string]time.Time
	order  []string
	seenN  int
	closed chan struct{}
	once   sync.Once
	// connected 标记当前是否与中继保持连接（受 mu 保护）。
	connected bool
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
	if c.seen == nil {
		c.seen = make(map[string]time.Time)
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

// execute 执行一条指令并回传 result。同一 id 只执行一次。
func (c *Client) execute(ctx context.Context, ws *websocket.Conn, env Envelope) {
	if c.duplicate(env.ID) {
		c.Logger("[relay] duplicate command %s ignored", env.ID)
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
	reply := &Envelope{
		V: ProtocolVersion, ID: newMessageID(), Kind: KindResult,
		TS: time.Now().UnixMilli(), ReplyTo: env.ID, Channel: env.Channel,
		Payload: mustJSON(result),
	}
	payload, _ := json.Marshal(reply)
	c.mu.Lock()
	err = ws.WriteMessage(websocket.TextMessage, payload)
	c.mu.Unlock()
	if err != nil {
		c.Logger("[relay] write result %s: %v", env.ID, err)
	}
}

// duplicate 记录已执行的 command id；返回 true 表示重复。
func (c *Client) duplicate(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[id]; ok {
		return true
	}
	c.seen[id] = time.Now()
	c.order = append(c.order, id)
	for len(c.order) > c.seenN {
		delete(c.seen, c.order[0])
		c.order = c.order[1:]
	}
	return false
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

// localRequest 是手机经中继发来的指令（chat / job / job_status / job_cancel）。
type localRequest struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	Agent string `json:"agent_id"`
	Dir   string `json:"work_dir"`
	// TaskID 用于 job_status / job_cancel。
	TaskID string `json:"task_id"`
	// IdempotencyKey 让手机端重试同一条指令时只落一个任务。
	IdempotencyKey string `json:"idempotency_key"`
}

// localJob 是电脑端 /api/jobs 的响应形状（只取中继需要的字段）。
type localJob struct {
	Task struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output string `json:"output"`
		Error  string `json:"error"`
		Task   string `json:"task"`
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
		OK: true, TaskID: out.Task.ID, Status: out.Task.Status,
		Reply: "已交给电脑后台执行（任务 " + shortID(out.Task.ID) + "）",
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
		reply = job.Task.Error
	}
	return ResultPayload{
		OK: job.Task.Error == "", Reply: reply, Error: job.Task.Error,
		TaskID: job.Task.ID, Status: job.Task.Status, Events: events,
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
