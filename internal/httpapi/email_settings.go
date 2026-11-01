package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"goldenfinger/agent/internal/settings"
)

type emailSettingsDTO struct {
	Enabled      bool   `json:"enabled"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	HasPassword  bool   `json:"has_password"`
	Mailbox      string `json:"mailbox"`
	TLS          bool   `json:"tls"`
	InitialFetch int    `json:"initial_fetch"`
	PollSeconds  int    `json:"poll_seconds"`
}

func toEmailSettingsDTO(value settings.Email) emailSettingsDTO {
	return emailSettingsDTO{
		Enabled: value.Enabled, Host: value.Host, Port: value.Port, Username: value.Username,
		Password: maskKey(value.Password), HasPassword: value.Password != "",
		Mailbox: value.Mailbox, TLS: value.TLS, InitialFetch: value.InitialFetch, PollSeconds: value.PollSeconds,
	}
}

func (s *Server) handleGetEmailSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "设置未启用")
		return
	}
	writeJSON(w, http.StatusOK, toEmailSettingsDTO(s.Settings.Email()))
}

func (s *Server) handlePutEmailSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "设置未启用")
		return
	}
	var req emailSettingsDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	current := s.Settings.Email()
	next := settings.Email{
		Enabled: req.Enabled, Host: strings.TrimSpace(req.Host), Port: req.Port,
		Username: strings.TrimSpace(req.Username), Mailbox: strings.TrimSpace(req.Mailbox),
		TLS: req.TLS, InitialFetch: req.InitialFetch, PollSeconds: req.PollSeconds,
		Password: req.Password,
	}
	if isMaskedOrEmpty(req.Password, current.Password) {
		next.Password = current.Password
	}
	if next.Enabled && (next.Host == "" || next.Username == "" || next.Password == "") {
		writeError(w, http.StatusBadRequest, "启用 IMAP 时 host、username、password 必填")
		return
	}
	if next.Port <= 0 || next.Port > 65535 {
		next.Port = 993
	}
	if next.Mailbox == "" {
		next.Mailbox = "INBOX"
	}
	if next.InitialFetch <= 0 || next.InitialFetch > 200 {
		next.InitialFetch = 20
	}
	if next.PollSeconds < 60 || next.PollSeconds > 3600 {
		next.PollSeconds = 300
	}
	if err := s.Settings.SaveEmail(next); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toEmailSettingsDTO(next))
}
