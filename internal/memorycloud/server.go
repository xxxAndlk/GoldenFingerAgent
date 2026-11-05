// Package memorycloud 实现云端共享长期记忆的 HTTPS API。
// 云端是共享记忆的主副本：端侧通过设备 Token 写入原始片段、检索派生事实、
// 执行遗忘；owner 永远由设备凭据解析，不信任请求体。
package memorycloud

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"goldenfinger/agent/internal/cloudauth"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
)

// Backend 是云端记忆所需的检索/遗忘/健康接口（*memory.Native 满足）。
type Backend interface {
	Search(ctx context.Context, query memory.MemoryQuery) ([]memory.MemoryFact, error)
	Forget(ctx context.Context, request memory.ForgetRequest) error
	Health(ctx context.Context) (memory.MemoryHealth, error)
}

// Config 装配云端记忆 API。
type Config struct {
	DB      *store.DB
	Backend Backend
	// Wake 在片段入库后唤醒后台抽取；可为 nil（下一轮轮询再处理）。
	Wake func()
	Now  func() time.Time
	// MaxEpisodeBytes 是单条片段正文字节上限，默认 64 KiB。
	MaxEpisodeBytes int
	Logger          func(format string, args ...any)
}

// Server 是云端记忆 HTTP 服务。
type Server struct {
	db              *store.DB
	repos           *store.Repos
	backend         Backend
	auth            *cloudauth.Authenticator
	wake            func()
	now             func() time.Time
	maxEpisodeBytes int
	logf            func(format string, args ...any)
}

// New 创建云端记忆服务。
func New(cfg Config) *Server {
	s := &Server{
		db:              cfg.DB,
		repos:           store.NewRepos(cfg.DB),
		backend:         cfg.Backend,
		wake:            cfg.Wake,
		now:             cfg.Now,
		maxEpisodeBytes: cfg.MaxEpisodeBytes,
		logf:            cfg.Logger,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.maxEpisodeBytes <= 0 {
		s.maxEpisodeBytes = 64 << 10
	}
	if s.logf == nil {
		s.logf = log.Printf
	}
	s.auth = &cloudauth.Authenticator{Repos: s.repos, Now: s.now}
	return s
}

// Handler 返回带路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/memory/v1/episodes", s.handleEpisode)
	mux.HandleFunc("POST /api/memory/v1/search", s.handleSearch)
	mux.HandleFunc("POST /api/memory/v1/forget", s.handleForget)
	mux.HandleFunc("GET /api/memory/v1/health", s.handleHealth)
	return s.withLogging(mux)
}

var (
	errUnauthorized = cloudauth.ErrUnauthorized
	errForbidden    = cloudauth.ErrForbidden
)

// authenticate 解析 Bearer Token 并校验 scope。
// 无效 Token 与已撤销设备统一返回 401，避免泄露资源是否存在。
func (s *Server) authenticate(r *http.Request, scope string) (*store.CloudDevice, error) {
	return s.auth.Authenticate(r.Context(), r, scope)
}

func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, scope string) (*store.CloudDevice, bool) {
	device, err := s.authenticate(r, scope)
	switch {
	case errors.Is(err, errForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return nil, false
	case err != nil:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, false
	}
	return device, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// 不记录正文、Token 与文件内容。
		s.logf("[memorycloud] %s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
