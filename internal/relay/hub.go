package relay

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Config 控制中继的资源上限。零值即默认值，全部可在 2C2G 机器上安全运行。
type Config struct {
	// MaxQueuePerSide 每个通道每个方向最多缓存的离线消息数。
	MaxQueuePerSide int
	// QueueTTL 离线消息最长保留时间；超时丢弃并计数。
	QueueTTL time.Duration
	// DedupTTL 消息 id 去重窗口。
	DedupTTL time.Duration
	// DedupSize 去重窗口内的最大 id 数。
	DedupSize int
	// CodeTTL 配对码有效期；过期后在宽限期内仍返回“已过期”，宽限后清除。
	CodeTTL time.Duration
	// MaxEndpoints 同时在线连接上限（手机 + 电脑）。
	MaxEndpoints int
	// MaxPayloadBytes 单条报文上限，防止小机器被大包打爆。
	MaxPayloadBytes int
	// Now 便于测试注入时钟。
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.MaxQueuePerSide <= 0 {
		c.MaxQueuePerSide = 200
	}
	if c.QueueTTL <= 0 {
		c.QueueTTL = 24 * time.Hour
	}
	if c.DedupTTL <= 0 {
		c.DedupTTL = 10 * time.Minute
	}
	if c.DedupSize <= 0 {
		c.DedupSize = 4096
	}
	if c.CodeTTL <= 0 {
		c.CodeTTL = 5 * time.Minute
	}
	if c.MaxEndpoints <= 0 {
		c.MaxEndpoints = 2000
	}
	if c.MaxPayloadBytes <= 0 {
		c.MaxPayloadBytes = 256 << 10
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// 身份角色。
const (
	RolePhone   = "phone"
	RoleDesktop = "desktop"
)

type desktop struct {
	id, token, name string
	createdAt       time.Time
}

type channelInfo struct {
	id, desktopID, phoneName, phoneToken string
	createdAt                            time.Time
}

type pairCode struct {
	code      string
	desktopID string
	expiresAt time.Time
}

// PairResult 是手机完成配对后拿到的凭据。
type PairResult struct {
	ChannelID   string `json:"channel_id"`
	PhoneToken  string `json:"phone_token"`
	DesktopID   string `json:"desktop_id"`
	DesktopName string `json:"desktop_name"`
}

// Endpoint 是一条已完成鉴权的 WebSocket 连接。
type Endpoint struct {
	role      string
	id        string // desktop id 或 channel id
	channel   string
	desktopID string

	send      chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	ws        *websocket.Conn
}

// Done 在连接关闭后关闭，供上层退出循环。
func (e *Endpoint) Done() <-chan struct{} { return e.closed }

// Role 返回 phone / desktop。
func (e *Endpoint) Role() string { return e.role }

// ID 返回 desktop id 或 channel id。
func (e *Endpoint) ID() string { return e.id }

// Close 关闭连接（幂等，不向 Send 阻塞）。
func (e *Endpoint) Close() {
	e.closeOnce.Do(func() {
		close(e.closed)
		if e.ws != nil {
			_ = e.ws.Close()
		}
	})
}

// Send 异步投递一条报文；缓冲满时断开连接，避免中继内存无界增长。
func (e *Endpoint) Send(data []byte) bool {
	select {
	case <-e.closed:
		return false
	default:
	}
	select {
	case e.send <- data:
		return true
	case <-e.closed:
		return false
	default:
		log.Printf("[relay] %s %s slow consumer, closing", e.role, e.id)
		e.Close()
		return false
	}
}

// Stats 是中继的可观测计数（不含任何消息内容）。
type Stats struct {
	Desktops   int            `json:"desktops"`
	Channels   int            `json:"channels"`
	Online     map[string]int `json:"online"`
	Queued     int            `json:"queued"`
	Dropped    int            `json:"dropped"`
	Duplicates int            `json:"duplicates"`
	Forwarded  int            `json:"forwarded"`
}

// Hub 是纯内存的中继状态机：配对关系、在线连接、离线队列与去重窗口。
type Hub struct {
	cfg Config

	mu       sync.Mutex
	desktops map[string]*desktop
	channels map[string]*channelInfo
	codes    map[string]*pairCode
	conns    map[string]*Endpoint // "role:id"
	queues   map[string][]*Envelope
	order    []string
	seen     map[string]time.Time

	dropped    int
	duplicates int
	forwarded  int
}

// NewHub 创建中继状态机。
func NewHub(cfg Config) *Hub {
	return &Hub{
		cfg:      cfg.withDefaults(),
		desktops: make(map[string]*desktop),
		channels: make(map[string]*channelInfo),
		codes:    make(map[string]*pairCode),
		conns:    make(map[string]*Endpoint),
		queues:   make(map[string][]*Envelope),
		seen:     make(map[string]time.Time),
	}
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func connKey(role, id string) string { return role + ":" + id }

func queueKey(channel, side string) string { return channel + "|" + side }

// RegisterDesktop 为电脑端签发 desktop_id 与 desktop_token。
// 中继按 id/token 隔离，任何人都无法访问他人的通道。
func (h *Hub) RegisterDesktop(name string) (string, string, error) {
	id, err := randomToken(8)
	if err != nil {
		return "", "", err
	}
	token, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	if name == "" {
		name = "电脑"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	if len(h.desktops) >= h.cfg.MaxEndpoints {
		return "", "", ErrTooMany
	}
	h.desktops[id] = &desktop{id: id, token: token, name: name, createdAt: h.cfg.Now()}
	log.Printf("[relay] desktop registered id=%s name=%q", id, name)
	return id, token, nil
}

func tokenEqual(a, b string) bool {
	if len(a) != len(b) || a == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// CreatePairCode 供电脑端生成一次性 6 位配对码展示给用户。
func (h *Hub) CreatePairCode(desktopID, token string) (string, time.Time, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	d, ok := h.desktops[desktopID]
	if !ok || !tokenEqual(d.token, token) {
		return "", time.Time{}, ErrUnauthorized
	}
	// 同一电脑只保留最新一个未用配对码，避免旧码长期滞留。
	for code, pc := range h.codes {
		if pc.desktopID == desktopID {
			delete(h.codes, code)
		}
	}
	code, err := numericCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := h.cfg.Now().Add(h.cfg.CodeTTL)
	h.codes[code] = &pairCode{code: code, desktopID: desktopID, expiresAt: expires}
	return code, expires, nil
}

func numericCode() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	n := int(buf[0])<<24 | int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("%06d", n%1000000), nil
}

// Pair 让手机凭配对码换取 phone_token，并绑定到对应电脑的通道。
func (h *Hub) Pair(code, phoneName string) (PairResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	pc, ok := h.codes[code]
	if !ok {
		return PairResult{}, ErrNotFound
	}
	if h.cfg.Now().After(pc.expiresAt) {
		delete(h.codes, code)
		return PairResult{}, ErrExpired
	}
	// 爆破防护由 HTTP 层按 IP 限流负责；这里不再累计错误次数，
	// 否则正常用户重复输错一次就会连带作废同一个码。
	d, ok := h.desktops[pc.desktopID]
	if !ok {
		delete(h.codes, code)
		return PairResult{}, ErrNotFound
	}
	channelID, err := randomToken(8)
	if err != nil {
		return PairResult{}, err
	}
	phoneToken, err := randomToken(32)
	if err != nil {
		return PairResult{}, err
	}
	if phoneName == "" {
		phoneName = "手机"
	}
	h.channels[channelID] = &channelInfo{
		id: channelID, desktopID: d.id, phoneName: phoneName,
		phoneToken: phoneToken, createdAt: h.cfg.Now(),
	}
	// 配对码单次有效。
	delete(h.codes, code)
	log.Printf("[relay] paired channel=%s desktop=%s phone=%q", channelID, d.id, phoneName)
	return PairResult{ChannelID: channelID, PhoneToken: phoneToken, DesktopID: d.id, DesktopName: d.name}, nil
}

// Authenticate 校验 WS 连接凭据，返回连接身份。
func (h *Hub) Authenticate(role, id, token string) (channel, desktopID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	switch role {
	case RoleDesktop:
		d, ok := h.desktops[id]
		if !ok || !tokenEqual(d.token, token) {
			return "", "", ErrUnauthorized
		}
		return "", d.id, nil
	case RolePhone:
		ch, ok := h.channels[id]
		if !ok || !tokenEqual(ch.phoneToken, token) {
			return "", "", ErrUnauthorized
		}
		return ch.id, ch.desktopID, nil
	default:
		return "", "", ErrUnauthorized
	}
}

// Attach 把连接登记为在线端点，并返回积压消息与 hello 报文。
func (h *Hub) Attach(ep *Endpoint) (*Envelope, []*Envelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	key := connKey(ep.role, ep.id)
	if old, ok := h.conns[key]; ok && old != ep {
		old.Close()
	}
	h.conns[key] = ep

	// 电脑端跨多个通道，需要把该电脑名下所有通道的待投消息都取出来。
	var pending []*Envelope
	if ep.role == RoleDesktop {
		channels := make([]*channelInfo, 0, 2)
		for _, ch := range h.channels {
			if ch.desktopID == ep.desktopID {
				channels = append(channels, ch)
			}
		}
		sort.Slice(channels, func(i, j int) bool { return channels[i].createdAt.Before(channels[j].createdAt) })
		for _, ch := range channels {
			qk := queueKey(ch.id, RoleDesktop)
			pending = append(pending, h.queues[qk]...)
			delete(h.queues, qk)
		}
		if len(channels) > 0 {
			ep.channel = channels[0].id
		}
	} else {
		qk := queueKey(ep.channel, ep.role)
		pending = h.queues[qk]
		delete(h.queues, qk)
	}

	hello := &Envelope{V: ProtocolVersion, Kind: KindHello, ID: "hello-" + key, TS: h.cfg.Now().UnixMilli(), Channel: ep.channel}
	payload := HelloPayload{Role: ep.role, Channel: ep.channel}
	if ep.role == RoleDesktop {
		payload.DesktopID = ep.desktopID
		if d, ok := h.desktops[ep.desktopID]; ok {
			payload.Desktop = d.name
		}
	} else if ch, ok := h.channels[ep.channel]; ok {
		payload.DesktopID = ch.desktopID
		if d, ok := h.desktops[ch.desktopID]; ok {
			payload.Desktop = d.name
		}
	}
	hello.Payload, _ = json.Marshal(payload)
	log.Printf("[relay] attached role=%s id=%s pending=%d", ep.role, ep.id, len(pending))
	return hello, pending
}

// Detach 移除连接（仅当仍是当前连接）。
func (h *Hub) Detach(ep *Endpoint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := connKey(ep.role, ep.id)
	if cur, ok := h.conns[key]; ok && cur == ep {
		delete(h.conns, key)
	}
}

// Handle 处理一条来自端点的报文：盖身份章、去重、投递或入队。
// 返回 nil 表示已接受（含重复）；错误只用于协议层问题。
func (h *Hub) Handle(ep *Endpoint, env *Envelope) error {
	if env == nil || env.ID == "" {
		return ErrNotFound
	}
	if env.Kind == KindPing {
		env.Kind = KindPong
		return nil // 由调用方在写路径回 pong
	}
	// 只有 command 从手机发出，result/ack/error 从电脑端发出。
	switch ep.role {
	case RolePhone:
		if env.Kind != KindCommand {
			return ErrUnauthorized
		}
	case RoleDesktop:
		if env.Kind != KindResult && env.Kind != KindError {
			return ErrUnauthorized
		}
	default:
		return ErrUnauthorized
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()

	// 电脑端可能配对了多部手机，因此 result/error 必须显式带上通道；
	// 只允许回给属于自己的通道，其余一律拒绝。
	channel := ep.channel
	if ep.role == RoleDesktop {
		channel = env.Channel
		ch, ok := h.channels[channel]
		if !ok || ch.desktopID != ep.desktopID {
			return ErrUnauthorized
		}
	}

	// 盖章：客户端自填的 channel/from/to 一律覆盖。
	env.V = ProtocolVersion
	env.Channel = channel
	if ep.role == RolePhone {
		env.From = "phone:" + channel
		env.To = "desktop:" + ep.desktopID
	} else {
		env.From = "desktop:" + ep.desktopID
		env.To = "phone:" + channel
	}
	if env.TS == 0 {
		env.TS = h.cfg.Now().UnixMilli()
	}

	if h.markSeenLocked(env.ID) {
		h.duplicates++
		data, _ := json.Marshal(&Envelope{V: ProtocolVersion, Kind: KindAck, ID: "ack-" + env.ID, TS: h.cfg.Now().UnixMilli(),
			Channel: channel, From: "relay", To: env.From, ReplyTo: env.ID,
			Payload: mustJSON(AckPayload{Status: "duplicate"})})
		ep.Send(data)
		return nil
	}

	targetRole := RoleDesktop
	if ep.role == RoleDesktop {
		targetRole = RolePhone
	}
	targetID := ep.desktopID
	if targetRole == RolePhone {
		targetID = channel
	}

	data, err := json.Marshal(env)
	if err != nil {
		return err
	}

	ack := &Envelope{V: ProtocolVersion, Kind: KindAck, ID: "ack-" + env.ID, TS: h.cfg.Now().UnixMilli(),
		Channel: channel, From: "relay", To: env.From, ReplyTo: env.ID}

	if target, ok := h.conns[connKey(targetRole, targetID)]; ok {
		if target.Send(data) {
			h.forwarded++
			ep.Send(mustJSONEnv(ack, AckPayload{Status: "accepted"}))
			return nil
		}
	}

	// 目标离线：入队（有上限 + TTL），并如实回执 queued，
	// 让手机端知道“已收下但还没被电脑执行”。
	qk := queueKey(channel, targetRole)
	batch := h.queues[qk]
	if len(batch) >= h.cfg.MaxQueuePerSide {
		// 丢最旧的，保留最新指令语义，并如实计数。
		batch = batch[1:]
		h.dropped++
	}
	h.queues[qk] = append(batch, env)
	ep.Send(mustJSONEnv(ack, AckPayload{Status: "accepted", Queued: true}))
	return nil
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func mustJSONEnv(env *Envelope, payload any) []byte {
	env.Payload = mustJSON(payload)
	raw, _ := json.Marshal(env)
	return raw
}

// markSeenLocked 记录消息 id；返回 true 表示重复。
func (h *Hub) markSeenLocked(id string) bool {
	now := h.cfg.Now()
	if _, ok := h.seen[id]; ok {
		return true
	}
	h.seen[id] = now
	h.order = append(h.order, id)
	for len(h.order) > h.cfg.DedupSize {
		old := h.order[0]
		h.order = h.order[1:]
		delete(h.seen, old)
	}
	return false
}

// pruneLocked 惰性清理过期配对码、离线消息与去重记录。
func (h *Hub) pruneLocked() {
	now := h.cfg.Now()
	for code, pc := range h.codes {
		// 过期后保留一个宽限期，让手机端拿到明确的“已过期”而不是“不存在”。
		if now.After(pc.expiresAt.Add(h.cfg.CodeTTL)) {
			delete(h.codes, code)
		}
	}
	for key, batch := range h.queues {
		kept := batch[:0]
		for _, env := range batch {
			if now.Sub(time.UnixMilli(env.TS)) > h.cfg.QueueTTL {
				h.dropped++
				continue
			}
			kept = append(kept, env)
		}
		if len(kept) == 0 {
			delete(h.queues, key)
			continue
		}
		h.queues[key] = kept
	}
	for id, at := range h.seen {
		if now.Sub(at) > h.cfg.DedupTTL {
			delete(h.seen, id)
		}
	}
	// 压缩去重顺序表，避免无界增长。
	if len(h.order) > h.cfg.DedupSize*2 {
		kept := h.order[:0]
		for _, id := range h.order {
			if _, ok := h.seen[id]; ok {
				kept = append(kept, id)
			}
		}
		h.order = kept
	}
}

// Stats 返回计数快照。
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	online := map[string]int{RoleDesktop: 0, RolePhone: 0}
	for _, ep := range h.conns {
		online[ep.role]++
	}
	queued := 0
	for _, batch := range h.queues {
		queued += len(batch)
	}
	return Stats{
		Desktops: len(h.desktops), Channels: len(h.channels), Online: online,
		Queued: queued, Dropped: h.dropped, Duplicates: h.duplicates, Forwarded: h.forwarded,
	}
}

// Channels 返回某台电脑下的通道摘要（供电脑端查看已配对手机）。
func (h *Hub) Channels(desktopID, token string) ([]map[string]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.desktops[desktopID]
	if !ok || !tokenEqual(d.token, token) {
		return nil, ErrUnauthorized
	}
	out := []map[string]string{}
	for _, ch := range h.channels {
		if ch.desktopID != desktopID {
			continue
		}
		out = append(out, map[string]string{
			"channel_id": ch.id,
			"phone_name": ch.phoneName,
			"created_at": ch.createdAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["created_at"] < out[j]["created_at"] })
	return out, nil
}

// RevokeChannel 解除某台手机与电脑的绑定（电脑端操作）。
func (h *Hub) RevokeChannel(desktopID, token, channelID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.desktops[desktopID]
	if !ok || !tokenEqual(d.token, token) {
		return ErrUnauthorized
	}
	ch, ok := h.channels[channelID]
	if !ok || ch.desktopID != desktopID {
		return ErrNotFound
	}
	delete(h.channels, channelID)
	if ep, ok := h.conns[connKey(RolePhone, channelID)]; ok {
		ep.Close()
		delete(h.conns, connKey(RolePhone, channelID))
	}
	delete(h.queues, queueKey(channelID, RoleDesktop))
	delete(h.queues, queueKey(channelID, RolePhone))
	log.Printf("[relay] revoked channel=%s desktop=%s", channelID, desktopID)
	return nil
}
