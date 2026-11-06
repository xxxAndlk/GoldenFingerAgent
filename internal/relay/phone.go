package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 手机端错误。
var (
	// ErrNotPaired 表示尚未与电脑配对。
	ErrNotPaired = errors.New("relay: 手机尚未与电脑配对")
	// ErrDesktopOffline 表示指令已在中继排队，但电脑当前不在线。
	ErrDesktopOffline = errors.New("relay: 电脑当前离线，指令已排队")
)

// PhoneState 是手机端保存的配对凭据（不含任何内容数据）。
type PhoneState struct {
	BaseURL    string `json:"base_url"`
	ChannelID  string `json:"channel_id"`
	PhoneToken string `json:"phone_token"`
	DesktopID  string `json:"desktop_id"`
	Desktop    string `json:"desktop_name"`
}

// Phone 是手机生活管家的中继客户端：配对、保持在线、发送指令并等待电脑结果。
//
// 它只负责“把一句话交给电脑执行并把结果带回来”，不做任务编排：
// 真正的执行、状态与幂等都在电脑端（agent_job / SQLite）。
type Phone struct {
	// StatePath 是配对凭据文件路径（App 私有目录）。
	StatePath string
	// SendTimeout 是等待电脑结果的默认上限。
	SendTimeout time.Duration
	// Logger 默认 log.Printf。
	Logger func(format string, args ...any)

	mu      sync.Mutex
	state   PhoneState
	loaded  bool
	conn    *websocket.Conn
	pending map[string]chan *Envelope
	closed  chan struct{}
	once    sync.Once
}

func (p *Phone) init() {
	if p.SendTimeout <= 0 {
		p.SendTimeout = 2 * time.Minute
	}
	if p.Logger == nil {
		p.Logger = log.Printf
	}
	if p.pending == nil {
		p.pending = make(map[string]chan *Envelope)
	}
	if p.closed == nil {
		p.closed = make(chan struct{})
	}
}

// Load 读取配对凭据；文件不存在时返回 nil。
func (p *Phone) Load() error {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loaded = true
	raw, err := os.ReadFile(p.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var st PhoneState
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("relay: 手机配对文件损坏: %w", err)
	}
	p.state = st
	return nil
}

func (p *Phone) save() error {
	if p.StatePath == "" {
		return errors.New("relay: 未配置手机凭据路径")
	}
	if err := os.MkdirAll(filepath.Dir(p.StatePath), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.StatePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.StatePath)
}

func (p *Phone) ensureLoaded() error {
	p.mu.Lock()
	loaded := p.loaded
	p.mu.Unlock()
	if loaded {
		return nil
	}
	return p.Load()
}

// Pair 用电脑上显示的配对码完成绑定，并持久化凭据。
func (p *Phone) Pair(ctx context.Context, baseURL, code, phoneName string) (PhoneState, error) {
	p.init()
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return PhoneState{}, errors.New("relay: 需要中继地址")
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return PhoneState{}, errors.New("relay: 中继地址必须以 http:// 或 https:// 开头")
	}
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(code), "phone_name": phoneName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/pair", bytes.NewReader(body))
	if err != nil {
		return PhoneState{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return PhoneState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var detail struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&detail)
		if detail.Error == "" {
			detail.Error = resp.Status
		}
		return PhoneState{}, fmt.Errorf("relay: 配对失败：%s", detail.Error)
	}
	var out struct {
		ChannelID   string `json:"channel_id"`
		PhoneToken  string `json:"phone_token"`
		DesktopID   string `json:"desktop_id"`
		DesktopName string `json:"desktop_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PhoneState{}, err
	}
	st := PhoneState{
		BaseURL: baseURL, ChannelID: out.ChannelID, PhoneToken: out.PhoneToken,
		DesktopID: out.DesktopID, Desktop: out.DesktopName,
	}
	p.mu.Lock()
	p.state = st
	p.loaded = true
	p.mu.Unlock()
	p.disconnect()
	if err := p.save(); err != nil {
		return st, err
	}
	return st, nil
}

// Status 返回配对状态（不含 token），供手机界面展示。
func (p *Phone) Status() map[string]any {
	if err := p.ensureLoaded(); err != nil {
		return map[string]any{"paired": false, "error": err.Error()}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	online := p.conn != nil
	return map[string]any{
		"paired":       p.state.ChannelID != "",
		"base_url":     p.state.BaseURL,
		"desktop_name": p.state.Desktop,
		"connected":    online,
	}
}

// RemoteStatus 报告手机到电脑的远程通道状态。
//
// 手机界面用它判断该读本机数据还是走电脑控制面。
func (p *Phone) RemoteStatus() map[string]any {
	st := p.Status()
	paired, _ := st["paired"].(bool)
	connected, _ := st["connected"].(bool)
	name, _ := st["desktop_name"].(string)
	return map[string]any{
		"paired": paired, "connected": connected, "desktop_name": name,
	}
}

// Unpair 清除本地配对凭据（中继侧由电脑端解除绑定）。
func (p *Phone) Unpair() error {
	p.disconnect()
	p.mu.Lock()
	p.state = PhoneState{}
	p.loaded = true
	p.mu.Unlock()
	if p.StatePath == "" {
		return nil
	}
	if err := os.Remove(p.StatePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (p *Phone) snapshot() PhoneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Close 关闭长连接。
func (p *Phone) Close() {
	p.once.Do(func() {
		if p.closed != nil {
			close(p.closed)
		}
	})
	p.disconnect()
}

func (p *Phone) disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	for id, ch := range p.pending {
		close(ch)
		delete(p.pending, id)
	}
}

func (p *Phone) wsURL(st PhoneState) string {
	base := st.BaseURL
	if strings.HasPrefix(base, "https://") {
		base = "wss://" + strings.TrimPrefix(base, "https://")
	} else {
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base + "/v1/ws?role=phone&id=" + st.ChannelID + "&pv=" + strconv.Itoa(ProtocolVersionV2)
}

// connect 建立与中继的 WS 连接，并启动读循环分发结果。
func (p *Phone) connect(ctx context.Context) error {
	p.mu.Lock()
	if p.conn != nil {
		p.mu.Unlock()
		return nil
	}
	st := p.state
	p.mu.Unlock()
	if st.ChannelID == "" || st.PhoneToken == "" {
		return ErrNotPaired
	}
	header := http.Header{}
	header.Set("X-Token", st.PhoneToken)
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	ws, _, err := dialer.DialContext(ctx, p.wsURL(st), header)
	if err != nil {
		return fmt.Errorf("relay: 连接中继失败: %w", err)
	}
	p.mu.Lock()
	p.conn = ws
	p.mu.Unlock()
	go p.readLoop(ws)
	return nil
}

func (p *Phone) readLoop(ws *websocket.Conn) {
	defer func() {
		p.mu.Lock()
		if p.conn == ws {
			p.conn = nil
		}
		p.mu.Unlock()
		_ = ws.Close()
	}()
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Kind {
		case KindAck:
			var ack AckPayload
			_ = json.Unmarshal(env.Payload, &ack)
			if ack.Queued {
				p.failPending(env.ReplyTo, ErrDesktopOffline)
			}
		case KindResult, KindError:
			p.deliver(env.ReplyTo, &env)
		case KindHello:
			p.Logger("[relay] phone online channel=%s", env.Channel)
		}
	}
}

func (p *Phone) deliver(id string, env *Envelope) {
	p.mu.Lock()
	ch, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.mu.Unlock()
	if ok {
		ch <- env
		close(ch)
	}
}

func (p *Phone) failPending(id string, err error) {
	p.mu.Lock()
	ch, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.mu.Unlock()
	if ok {
		ch <- &Envelope{Kind: KindError, ReplyTo: id, Payload: mustJSON(map[string]string{"error": err.Error()})}
		close(ch)
	}
}

// Send 把一条指令交给电脑执行并等待结果（自动生成幂等键）。
func (p *Phone) Send(ctx context.Context, payload any) (ResultPayload, error) {
	return p.SendWithID(ctx, "", payload)
}

// SendWithID 用调用方提供的幂等键发送指令。
//
// 幂等键的意义：手机端超时后重试必须复用同一个 id，电脑端才会按 id 去重，
// 避免“点了重试就执行两次”。id 为空时自动生成。
//
// 返回 ResultPayload 描述电脑的执行结果；若中继回执 queued（电脑离线），
// 立刻返回 ErrDesktopOffline，让界面如实告诉用户“已排队，等电脑上线”。
func (p *Phone) SendWithID(ctx context.Context, id string, payload any) (ResultPayload, error) {
	p.init()
	return p.send(ctx, id, payload, p.SendTimeout)
}

// send 是 SendWithID 的实际实现，timeout 显式传入以便不同指令用不同等待上限。
//
// id 是这条指令的幂等键，会写进载荷；信封 ID 每次尝试都换新的。
// 换新的原因是中继按信封 ID 去重：重投若沿用旧信封 ID 会被中继当重复丢掉，
// 手机永远拿不到结果。电脑按载荷里的幂等键判断是否已经执行过。
func (p *Phone) send(ctx context.Context, id string, payload any, timeout time.Duration) (ResultPayload, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if err := p.ensureLoaded(); err != nil {
		return ResultPayload{}, err
	}
	st := p.snapshot()
	if st.ChannelID == "" {
		return ResultPayload{}, ErrNotPaired
	}
	if err := p.connect(ctx); err != nil {
		return ResultPayload{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ResultPayload{}, err
	}
	if id == "" {
		id = newMessageID()
	}
	raw, err = withIdempotencyKey(raw, id)
	if err != nil {
		return ResultPayload{}, err
	}
	envelopeID := newMessageID()
	ch := make(chan *Envelope, 1)
	p.mu.Lock()
	ws := p.conn
	p.pending[envelopeID] = ch
	p.mu.Unlock()
	if ws == nil {
		return ResultPayload{}, errors.New("relay: 连接已断开")
	}
	frame, _ := json.Marshal(&Envelope{V: ProtocolVersion, ID: envelopeID, Kind: KindCommand, TS: time.Now().UnixMilli(), Payload: raw})
	p.mu.Lock()
	err = ws.WriteMessage(websocket.TextMessage, frame)
	p.mu.Unlock()
	if err != nil {
		p.failPending(envelopeID, err)
		return ResultPayload{}, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-waitCtx.Done():
		p.mu.Lock()
		delete(p.pending, envelopeID)
		p.mu.Unlock()
		return ResultPayload{}, errors.New("relay: 电脑没有在预期时间内返回结果")
	case env, ok := <-ch:
		if !ok || env == nil {
			return ResultPayload{}, ErrDesktopOffline
		}
		if env.Kind == KindError {
			var detail struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(env.Payload, &detail)
			if detail.Error == "" {
				detail.Error = "电脑返回了错误"
			}
			if strings.Contains(detail.Error, ErrDesktopOffline.Error()) {
				return ResultPayload{}, ErrDesktopOffline
			}
			return ResultPayload{}, errors.New("relay: " + detail.Error)
		}
		var result ResultPayload
		if err := json.Unmarshal(env.Payload, &result); err != nil {
			return ResultPayload{}, err
		}
		return result, nil
	}
}

// withIdempotencyKey 保证载荷里带上限等键。
// 已经有值时不覆盖：调用方显式给出的幂等键优先。
func withIdempotencyKey(raw []byte, key string) ([]byte, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	if existing, _ := fields["idempotency_key"].(string); strings.TrimSpace(existing) == "" {
		fields["idempotency_key"] = key
	}
	return json.Marshal(fields)
}

// JobRequest 描述一条交给电脑后台执行的长任务。
type JobRequest struct {
	Text    string `json:"text"`
	AgentID string `json:"agent_id,omitempty"`
	WorkDir string `json:"work_dir,omitempty"`
	// ClientID 是幂等键：重试同一条长任务不会在电脑上产生两个任务。
	ClientID string `json:"client_id,omitempty"`
}

// SubmitJob 让电脑把任务放进持久化队列并立刻返回任务 id。
//
// 长任务不占用一次请求：手机先拿到 task_id，再用 JobStatus 轮询进度，
// 电脑重启也不会把任务丢掉（重启后标记 interrupted 等待核对，不盲目重跑）。
func (p *Phone) SubmitJob(ctx context.Context, req JobRequest) (ResultPayload, error) {
	payload := map[string]any{
		"kind": "job", "text": req.Text,
		"idempotency_key": stringOr2(req.ClientID, newMessageID()),
	}
	if req.AgentID != "" {
		payload["agent_id"] = req.AgentID
	}
	if req.WorkDir != "" {
		payload["work_dir"] = req.WorkDir
	}
	return p.sendKind(ctx, req.ClientID, payload, 60*time.Second)
}

// JobStatus 查询任务状态与进度事件。
func (p *Phone) JobStatus(ctx context.Context, taskID string) (ResultPayload, error) {
	return p.sendKind(ctx, "", map[string]any{"kind": "job_status", "task_id": taskID}, 30*time.Second)
}

// CancelJob 请求电脑取消仍在排队/执行中的任务。
func (p *Phone) CancelJob(ctx context.Context, taskID string) (ResultPayload, error) {
	return p.sendKind(ctx, "", map[string]any{"kind": "job_cancel", "task_id": taskID}, 30*time.Second)
}

// sendKind 用给定的等待上限发送一条中继指令。
func (p *Phone) sendKind(ctx context.Context, id string, payload any, timeout time.Duration) (ResultPayload, error) {
	p.init()
	return p.send(ctx, id, payload, timeout)
}

func stringOr2(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
