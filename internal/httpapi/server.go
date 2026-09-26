// Package httpapi 是线上层（相当于 pi 的 packages/protocol）：
// JSON DTO + 覆盖 agent 运行时与领域服务的 HTTP 处理器。
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"
	"unicode/utf8"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
)

// Server 在运行时之上装配 HTTP 路由（无框架——标准库 mux）。
type Server struct {
	Repos   *store.Repos
	Runtime *agent.Runtime
	WebDir  string
	Now     func() time.Time
	// Settings 保存可运行时编辑的模型设置（JSON 文件）；nil 表示禁用。
	Settings *settings.Store
	// FallbackLLM 描述 config.yaml 的对话模型，在没有运行时覆盖时
	// 显示在设置 UI 中。
	FallbackLLM settings.LLM
	// Outbox 收集 "app" 渠道的提醒投递，供 Web UI 轮询。
	Outbox *Outbox
}

// Outbox 是已投递提醒文本的小型内存队列（开发 MVP）。
type Outbox struct {
	items []OutboxItem
}

type OutboxItem struct {
	UserID string `json:"user_id"`
	Body   string `json:"body"`
	At     string `json:"at"`
}

func (o *Outbox) Push(userID, body string, at time.Time) {
	o.items = append(o.items, OutboxItem{UserID: userID, Body: body, At: at.Format("15:04")})
}

func (o *Outbox) Drain(userID string) []OutboxItem {
	var out []OutboxItem
	var rest []OutboxItem
	for _, it := range o.items {
		if it.UserID == userID {
			out = append(out, it)
		} else {
			rest = append(rest, it)
		}
	}
	o.items = rest
	return out
}

// Handler 构建包含全部路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("GET /api/sessions/{id}/messages", s.handleSessionMessages)
	mux.HandleFunc("GET /api/persons", s.handleListPersons)
	mux.HandleFunc("GET /api/persons/{id}", s.handleGetPerson)
	mux.HandleFunc("PUT /api/persons/{id}", s.handleUpdatePerson)
	mux.HandleFunc("DELETE /api/persons/{id}", s.handleForgetPerson)
	mux.HandleFunc("DELETE /api/facts/{id}", s.handleForgetFact)
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("POST /api/tasks/{id}/{action}", s.handleTaskAction)
	mux.HandleFunc("GET /api/intents", s.handleListIntents)
	mux.HandleFunc("DELETE /api/intents/{id}", s.handleCancelIntent)
	mux.HandleFunc("GET /api/memory/export.md", s.handleMemoryExport)
	mux.HandleFunc("GET /api/reminders", s.handleReminders)
	mux.HandleFunc("GET /api/outbox", s.handleOutbox)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/me/consent", s.handleConsent)
	mux.HandleFunc("GET /api/settings/llm", s.handleGetLLMSettings)
	mux.HandleFunc("PUT /api/settings/llm", s.handlePutLLMSettings)
	mux.HandleFunc("POST /api/voice/transcribe", s.handleTranscribe)
	mux.HandleFunc("POST /api/voice/speak", s.handleSpeak)

	// 静态 Web UI。
	mux.Handle("GET /", http.FileServer(http.Dir(s.WebDir)))

	return s.withLogging(mux)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newReqID()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), reqIDKey, id)))
		log.Printf("[http] %s %s %s → %d (%s)", id, r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

// statusWriter 为访问日志捕获响应状态码。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// reqID 关联同一 HTTP 请求内产生的所有日志行。
type ctxKey string

const reqIDKey ctxKey = "reqid"

func newReqID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func reqID(ctx context.Context) string {
	if v, ok := ctx.Value(reqIDKey).(string); ok {
		return v
	}
	return "------"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// ---- 辅助函数 ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// currentUser 从 X-User-Id 请求头解析演示用户，否则回退到第一个
// （自动预置的）用户。MVP 中认证是桩。
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) *store.User {
	ctx := r.Context()
	if id := r.Header.Get("X-User-Id"); id != "" {
		u, err := s.Repos.Users.Get(ctx, id)
		if err == nil {
			return u
		}
	}
	// 演示回退：自动预置一个普通用户。
	users, err := s.Repos.Users.ListAll(ctx)
	if err == nil && len(users) > 0 {
		return &users[0]
	}
	u := &store.User{UserType: store.UserGeneral, Name: "演示用户", TZ: "Asia/Shanghai"}
	if err := s.Repos.Users.Create(ctx, u); err != nil {
		writeError(w, 500, err.Error())
		return nil
	}
	return u
}
