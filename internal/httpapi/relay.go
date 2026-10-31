package httpapi

import (
	"context"
	"net"
	"net/http"
	"time"
)

// RelayControl 是电脑端中继连接的抽象，便于在无中继（单机模式）时降级。
type RelayControl interface {
	PairCode(ctx context.Context) (string, time.Time, error)
	Channels(ctx context.Context) ([]map[string]string, error)
	Revoke(ctx context.Context, channelID string) error
	Status() map[string]any
}

func (s *Server) requireRelay(w http.ResponseWriter, r *http.Request) bool {
	if s.Relay == nil {
		writeError(w, 503, "手机连接未启用（未配置中继）")
		return false
	}
	// 配对码等价于“允许远端控制本机”，只允许本机界面获取；
	// 本机服务默认没有鉴权，绝不能把该能力开放到局域网。
	if !isLoopback(r) {
		writeError(w, 403, "该接口仅允许本机访问")
		return false
	}
	return true
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// POST /api/relay/pair-code — 生成一次性配对码，供手机输入。
func (s *Server) handleRelayPairCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireRelay(w, r) {
		return
	}
	code, expires, err := s.Relay.PairCode(r.Context())
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"code":       code,
		"expires_at": expires.UTC().Format(time.RFC3339),
	})
}

// GET /api/relay/status — 中继连接状态（不含 token）。
func (s *Server) handleRelayStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireRelay(w, r) {
		return
	}
	writeJSON(w, 200, s.Relay.Status())
}

// GET /api/relay/channels — 已配对手机。
func (s *Server) handleRelayChannels(w http.ResponseWriter, r *http.Request) {
	if !s.requireRelay(w, r) {
		return
	}
	channels, err := s.Relay.Channels(r.Context())
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"channels": channels})
}

// DELETE /api/relay/channels/{id} — 解除某台手机的绑定。
func (s *Server) handleRelayRevokeChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireRelay(w, r) {
		return
	}
	if err := s.Relay.Revoke(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}
