package relay

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Server 把 Hub 暴露为 HTTP/WS 接口。它不保存任何业务数据。
type Server struct {
	Hub *Hub
	cfg Config

	upgrader websocket.Upgrader
	limits   *limiter
}

// NewServer 创建中继 HTTP 服务。
func NewServer(hub *Hub, cfg Config) *Server {
	cfg = cfg.withDefaults()
	s := &Server{Hub: hub, cfg: cfg, limits: newLimiter(cfg.Now)}
	s.upgrader = websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		// 中继的连接不是浏览器页面，鉴权靠 token；Origin 不作为安全边界。
		CheckOrigin: func(*http.Request) bool { return true },
	}
	return s
}

// Handler 返回路由表。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/desktop/register", s.handleRegisterDesktop)
	mux.HandleFunc("POST /v1/pair/code", s.handleCreatePairCode)
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	mux.HandleFunc("GET /v1/channels", s.handleChannels)
	mux.HandleFunc("DELETE /v1/channels/{id}", s.handleRevokeChannel)
	mux.HandleFunc("GET /v1/ws", s.handleWS)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// desktopCredentials 从请求中读取电脑端凭据（头优先，其次 query）。
func desktopCredentials(r *http.Request) (string, string) {
	id := r.Header.Get("X-Desktop-Id")
	token := r.Header.Get("X-Desktop-Token")
	if token == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if id == "" {
		id = r.URL.Query().Get("desktop_id")
	}
	if token == "" {
		token = r.URL.Query().Get("desktop_token")
	}
	return id, token
}

func (s *Server) handleRegisterDesktop(w http.ResponseWriter, r *http.Request) {
	if !s.limits.allow(clientIP(r), "register", 20, time.Minute) {
		writeErr(w, http.StatusTooManyRequests, ErrTooMany.Error())
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	id, token, err := s.Hub.RegisterDesktop(req.Name)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"desktop_id": id, "desktop_token": token})
}

func (s *Server) handleCreatePairCode(w http.ResponseWriter, r *http.Request) {
	if !s.limits.allow(clientIP(r), "paircode", 30, time.Minute) {
		writeErr(w, http.StatusTooManyRequests, ErrTooMany.Error())
		return
	}
	id, token := desktopCredentials(r)
	code, expires, err := s.Hub.CreatePairCode(id, token)
	switch {
	case err == ErrUnauthorized:
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       code,
		"expires_at": expires.UTC().Format(time.RFC3339),
		"ttl_sec":    int(s.cfg.CodeTTL.Seconds()),
	})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if !s.limits.allow(clientIP(r), "pair", 10, time.Minute) {
		writeErr(w, http.StatusTooManyRequests, ErrTooMany.Error())
		return
	}
	var req struct {
		Code      string `json:"code"`
		PhoneName string `json:"phone_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	result, err := s.Hub.Pair(req.Code, req.PhoneName)
	switch {
	case err == ErrNotFound:
		writeErr(w, http.StatusNotFound, "配对码不存在或已使用")
		return
	case err == ErrExpired:
		writeErr(w, http.StatusGone, "配对码已过期，请在电脑端重新获取")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	id, token := desktopCredentials(r)
	channels, err := s.Hub.Channels(id, token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}

func (s *Server) handleRevokeChannel(w http.ResponseWriter, r *http.Request) {
	id, token := desktopCredentials(r)
	err := s.Hub.RevokeChannel(id, token, r.PathValue("id"))
	switch {
	case err == ErrUnauthorized:
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	case err == ErrNotFound:
		writeErr(w, http.StatusNotFound, "通道不存在")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleHealth 只暴露计数，便于 2C2G 机器上用 curl 做存活与容量检查。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"version":   ProtocolVersion,
		"uptime_ms": time.Since(startedAt).Milliseconds(),
		"stats":     s.Hub.Stats(),
	})
}

var startedAt = time.Now()

// wsToken 从 header / query 取 token。
func wsToken(r *http.Request) string {
	if t := r.Header.Get("X-Token"); t != "" {
		return t
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if t := r.Header.Get("X-Desktop-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}

// handleWS 是手机与电脑共用的 WebSocket 入口。
//
//	phone:   GET /v1/ws?role=phone&id=<channel_id>&token=<phone_token>
//	desktop: GET /v1/ws?role=desktop&id=<desktop_id>&token=<desktop_token>
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	id := r.URL.Query().Get("id")
	if id == "" {
		id = r.URL.Query().Get("channel_id")
	}
	if id == "" && role == RoleDesktop {
		id = r.URL.Query().Get("desktop_id")
	}
	token := wsToken(r)
	channel, desktopID, err := s.Hub.Authenticate(role, id, token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// ?pv=2 声明支持 v2（ArtifactRef）；缺省视为 v1，保证旧端不受影响。
	version := ProtocolVersion
	if raw := r.URL.Query().Get("pv"); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil && parsed >= ProtocolVersion {
			version = parsed
			if version > ProtocolVersionV2 {
				version = ProtocolVersionV2
			}
		}
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade 已写响应
	}
	ep := &Endpoint{
		role: role, id: id, channel: channel, desktopID: desktopID, version: version, ws: ws,
		send: make(chan []byte, 32), closed: make(chan struct{}),
	}
	defer func() {
		s.Hub.Detach(ep)
		ep.Close()
	}()

	hello, pending := s.Hub.Attach(ep)
	go s.writePump(ep)
	ep.Send(mustMarshal(hello))
	for _, env := range pending {
		ep.Send(mustMarshal(env))
	}

	ws.SetReadLimit(int64(s.cfg.MaxPayloadBytes))
	_ = ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	})
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			ep.Send(mustMarshal(&Envelope{V: ProtocolVersion, Kind: KindError, ID: "err-" + id,
				TS: time.Now().UnixMilli(), From: "relay", To: role + ":" + id,
				Payload: mustJSON(map[string]string{"error": "bad envelope"})}))
			continue
		}
		if len(env.Payload) > s.cfg.MaxPayloadBytes {
			ep.Send(mustMarshal(&Envelope{V: ProtocolVersion, Kind: KindError, ID: "err-" + env.ID,
				TS: time.Now().UnixMilli(), From: "relay", To: role + ":" + id, ReplyTo: env.ID,
				Payload: mustJSON(map[string]string{"error": ErrPayloadTooBig.Error()})}))
			continue
		}
		if env.Kind == KindPing {
			ep.Send(mustMarshal(&Envelope{V: ProtocolVersion, Kind: KindPong, ID: "pong-" + env.ID,
				TS: time.Now().UnixMilli(), From: "relay", To: role + ":" + id, ReplyTo: env.ID}))
			continue
		}
		if err := s.Hub.Handle(ep, &env); err != nil {
			ep.Send(mustMarshal(&Envelope{V: ProtocolVersion, Kind: KindError, ID: "err-" + env.ID,
				TS: time.Now().UnixMilli(), From: "relay", To: role + ":" + id, ReplyTo: env.ID,
				Payload: mustJSON(map[string]string{"error": err.Error()})}))
		}
	}
}

func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		log.Printf("[relay] marshal: %v", err)
		return []byte(`{}`)
	}
	return raw
}

// writePump 串行写，带写超时与 30s 控制帧 ping。
func (s *Server) writePump(ep *Endpoint) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ep.closed:
			return
		case data := <-ep.send:
			_ = ep.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ep.ws.WriteMessage(websocket.TextMessage, data); err != nil {
				ep.Close()
				return
			}
		case <-ticker.C:
			_ = ep.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ep.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				ep.Close()
				return
			}
		}
	}
}

// limiter 是基于内存的按 IP 桶限流，防止配对码被爆破。
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, buckets: make(map[string]*bucket)}
}

func (l *limiter) allow(ip, kind string, max int, window time.Duration) bool {
	key := kind + "|" + ip
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > 10000 {
		for k, b := range l.buckets {
			if now.After(b.reset) {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = &bucket{count: 1, reset: now.Add(window)}
		return true
	}
	if b.count >= max {
		return false
	}
	b.count++
	return true
}
