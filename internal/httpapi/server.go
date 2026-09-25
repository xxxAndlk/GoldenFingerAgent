// Package httpapi is the wire layer (pi's packages/protocol analog):
// JSON DTOs + HTTP handlers over the agent runtime and domain services.
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

// Server wires HTTP routes over the runtime (no framework — stdlib mux).
type Server struct {
	Repos   *store.Repos
	Runtime *agent.Runtime
	WebDir  string
	Now     func() time.Time
	// Settings holds runtime-editable model settings (JSON file); nil disables.
	Settings *settings.Store
	// FallbackLLM describes the config.yaml chat model, shown in settings UI
	// when no runtime override exists.
	FallbackLLM settings.LLM
	// Outbox collects "app"-channel reminder deliveries for polling by the web UI.
	Outbox *Outbox
}

// Outbox is a tiny in-memory queue of delivered reminder texts (dev MVP).
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

// Handler builds the http.Handler with all routes.
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

	// Static web UI.
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

// statusWriter captures the response status code for access logs.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// reqID correlates all log lines produced within one HTTP request.
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

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// currentUser resolves the demo user from the X-User-Id header, or falls back
// to the first (auto-provisioned) user. Auth is a stub in the MVP.
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) *store.User {
	ctx := r.Context()
	if id := r.Header.Get("X-User-Id"); id != "" {
		u, err := s.Repos.Users.Get(ctx, id)
		if err == nil {
			return u
		}
	}
	// Demo fallback: auto-provision a general user.
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
