// Package deliverycloud 实现云端临时文件交付：上传、收件箱、下载、ACK、
// 撤销与 TTL 清理。文件正文只做临时传输，不做索引或内容理解。
package deliverycloud

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"goldenfinger/agent/internal/cloudauth"
	"goldenfinger/agent/internal/store"
)

type Config struct {
	DB       *store.DB
	Dir      string
	TTL      time.Duration
	MaxBytes int64
	Now      func() time.Time
	Logger   func(format string, args ...any)
}

type Server struct {
	db       *store.DB
	repos    *store.Repos
	auth     *cloudauth.Authenticator
	dir      string
	tmpDir   string
	ttl      time.Duration
	maxBytes int64
	now      func() time.Time
	logf     func(format string, args ...any)
}

func New(cfg Config) *Server {
	s := &Server{
		db: cfg.DB, repos: store.NewRepos(cfg.DB), dir: cfg.Dir,
		ttl: cfg.TTL, maxBytes: cfg.MaxBytes, now: cfg.Now, logf: cfg.Logger,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.ttl <= 0 {
		s.ttl = 72 * time.Hour
	}
	if s.maxBytes <= 0 {
		s.maxBytes = 100 << 20
	}
	if s.logf == nil {
		s.logf = log.Printf
	}
	s.tmpDir = filepath.Join(s.dir, "tmp")
	s.auth = &cloudauth.Authenticator{Repos: s.repos, Now: s.now}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/delivery/v1/artifacts", s.handleUpload)
	mux.HandleFunc("GET /api/delivery/v1/inbox", s.handleInbox)
	mux.HandleFunc("GET /api/delivery/v1/artifacts/{id}", s.handleGet)
	mux.HandleFunc("GET /api/delivery/v1/artifacts/{id}/content", s.handleContent)
	mux.HandleFunc("POST /api/delivery/v1/artifacts/{id}/ack", s.handleAck)
	mux.HandleFunc("DELETE /api/delivery/v1/artifacts/{id}", s.handleDelete)
	return mux
}

func (s *Server) require(w http.ResponseWriter, r *http.Request, scope string) (*store.CloudDevice, bool) {
	device, err := s.auth.Authenticate(r.Context(), r, scope)
	switch {
	case errors.Is(err, cloudauth.ErrForbidden):
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

// StartCleaner 定期物理删除过期交付；ctx 取消后退出。
func (s *Server) StartCleaner(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := s.Cleanup(ctx); n > 0 {
				s.logf("[deliverycloud] expired %d delivery file(s)", n)
			}
		}
	}
}

// Cleanup 删除已过期交付并标记状态；返回清理数量。
func (s *Server) Cleanup(ctx context.Context) int {
	rows, err := s.repos.CloudDeliveries.Expired(ctx, s.now(), 200)
	if err != nil {
		s.logf("[deliverycloud] expired query failed: %v", err)
		return 0
	}
	cleaned := 0
	for _, item := range rows {
		if item.StoragePath != "" {
			if err := os.Remove(item.StoragePath); err != nil && !os.IsNotExist(err) {
				s.logf("[deliverycloud] remove %s failed: %v", item.DeliveryID, err)
				continue
			}
		}
		if err := s.repos.CloudDeliveries.MarkExpired(ctx, item.DeliveryID, s.now()); err != nil {
			s.logf("[deliverycloud] mark expired %s failed: %v", item.DeliveryID, err)
			continue
		}
		cleaned++
	}
	return cleaned
}
