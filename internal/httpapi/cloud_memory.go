package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/settings"
)

type cloudMemoryDTO struct {
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	DeviceID        string `json:"device_id"`
	OwnerID         string `json:"owner_user_id"`
	Token           string `json:"token"`
	HasToken        bool   `json:"has_token"`
	DeliveryEnabled bool   `json:"delivery_enabled"`
	Mode            string `json:"mode"` // cloud | native
	Ready           bool   `json:"ready"`
	Detail          string `json:"detail"`
	Pending         int    `json:"pending"`
	Failed          int    `json:"failed"`
	Paused          int    `json:"paused"`
	RestartRequired bool   `json:"restart_required,omitempty"`
}

func (s *Server) cloudMemoryConfig() settings.CloudMemory {
	if s.Settings == nil {
		return settings.CloudMemory{}
	}
	return s.Settings.CloudMemory()
}

func (s *Server) cloudMemorySnapshot() cloudMemoryDTO {
	cfg := s.cloudMemoryConfig()
	hasToken := s.Settings != nil && s.Settings.HasCloudToken()
	dto := cloudMemoryDTO{
		Enabled: cfg.Enabled, BaseURL: cfg.BaseURL, DeviceID: cfg.DeviceID,
		OwnerID: cfg.OwnerID, Token: "", HasToken: hasToken,
		DeliveryEnabled: cfg.DeliveryEnabled, Mode: "native",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner := cfg.OwnerID
	if owner == "" {
		if users, err := s.Repos.Users.ListAll(ctx); err == nil && len(users) > 0 {
			owner = users[0].ID
		}
	}
	if owner != "" {
		if pending, failed, paused, err := s.Repos.MemoryOutbox.Counts(ctx, owner); err == nil {
			dto.Pending, dto.Failed, dto.Paused = pending, failed, paused
		}
	}
	if s.RemoteMemory != nil {
		dto.Mode = "cloud"
		if health, err := s.RemoteMemory.Health(ctx); err == nil {
			dto.Ready = health.Ready
			dto.Detail = health.Detail
		} else {
			dto.Detail = err.Error()
		}
	}
	return dto
}

func (s *Server) handleGetCloudMemory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cloudMemorySnapshot())
}

type cloudMemoryRequest struct {
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	DeviceID        string `json:"device_id"`
	OwnerID         string `json:"owner_user_id"`
	Token           string `json:"token"`
	DeliveryEnabled bool   `json:"delivery_enabled"`
}

func (s *Server) handlePutCloudMemory(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, 500, "settings store not configured")
		return
	}
	var req cloudMemoryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if req.BaseURL != "" && !strings.HasPrefix(req.BaseURL, "http://") &&
		!strings.HasPrefix(req.BaseURL, "https://") {
		writeError(w, 400, "云端地址必须以 http:// 或 https:// 开头")
		return
	}
	current := s.cloudMemoryConfig()
	next := settings.CloudMemory{
		Enabled: req.Enabled, BaseURL: req.BaseURL,
		DeviceID: strings.TrimSpace(req.DeviceID), OwnerID: strings.TrimSpace(req.OwnerID),
		DeliveryEnabled: req.DeliveryEnabled,
		TokenRef:        current.TokenRef,
	}
	newToken := strings.TrimSpace(req.Token)
	if strings.HasPrefix(newToken, "****") {
		newToken = ""
	}
	switch {
	case newToken != "":
		if err := s.Settings.SaveCloudToken(newToken); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		next.TokenRef = "cloud-token.bin"
	case current.Token != "" && current.TokenRef == "":
		// 旧版本明文 Token 迁移到加密存储。
		if err := s.Settings.SaveCloudToken(current.Token); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		next.TokenRef = "cloud-token.bin"
	}
	if next.Enabled && (next.BaseURL == "" || !s.Settings.HasCloudToken()) {
		writeError(w, 400, "启用云端共享记忆需要地址和设备 Token")
		return
	}
	if err := s.Settings.SaveCloudMemory(next); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	dto := s.cloudMemorySnapshot()
	dto.RestartRequired = true
	writeJSON(w, 200, dto)
}

type cloudMemoryTestRequest struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

func (s *Server) handleTestCloudMemory(w http.ResponseWriter, r *http.Request) {
	var req cloudMemoryTestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	current := s.cloudMemoryConfig()
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		baseURL = current.BaseURL
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || strings.HasPrefix(token, "****") {
		token = s.Settings.CloudToken()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	health, err := memory.ProbeRemote(ctx, baseURL, token, 8*time.Second)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ready": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ready": health.Ready, "backend": health.Backend, "detail": health.Detail,
	})
}

func (s *Server) handleCloudMemoryStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cloudMemorySnapshot())
}
