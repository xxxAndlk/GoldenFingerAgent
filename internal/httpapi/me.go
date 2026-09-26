package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

func loadTZ(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.FixedZone(tz, 8*3600)
	}
	return loc
}

// handleMe 返回当前用户的资料（认证是演示用桩）。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	writeJSON(w, 200, map[string]any{
		"id":        u.ID,
		"user_type": u.UserType,
		"name":      u.Name,
		"tz":        u.TZ,
	})
}

type consentRequest struct {
	Scope     string `json:"scope"`
	Guardian  string `json:"guardian_id"`
	SubjectID string `json:"subject_user_id"`
}

// handleConsent 记录针对儿童账号的监护人同意（文档 §9）。
func (s *Server) handleConsent(w http.ResponseWriter, r *http.Request) {
	var req consentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	if req.Scope == "" {
		req.Scope = "basic"
	}
	ctx := r.Context()
	subject := req.SubjectID
	if subject == "" {
		u := s.currentUser(w, r)
		if u == nil {
			return
		}
		subject = u.ID
	}
	var guardian *string
	if req.Guardian != "" {
		guardian = &req.Guardian
	}
	c, err := s.Repos.Consents.Grant(ctx, subject, req.Scope, guardian)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	owner := subject
	_ = s.Repos.Audit.Append(ctx, &owner, "guardian", "consent_grant", req.Scope, nil)
	writeJSON(w, 200, map[string]any{"status": "granted", "consent": c})
}

type transcribeRequest struct {
	AudioB64 string `json:"audio_b64"`
	Format   string `json:"format"`
}

// handleTranscribe 是 ASR 桩端点（语音接口占位）。
func (s *Server) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	var req transcribeRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	// 桩响应；真实的 ASR 服务可接入 extsvc.ASRService 背后。
	writeJSON(w, 200, map[string]string{"text": "（语音识别 stub：这是一段模拟的转写文本）"})
}

type speakRequest struct {
	Text string `json:"text"`
}

// handleSpeak 是 TTS 桩端点（语音接口占位）。
func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	var req speakRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		writeError(w, 400, "text is required")
		return
	}
	writeJSON(w, 200, map[string]string{
		"audio_b64": "",
		"format":    "wav",
		"text":      req.Text,
		"note":      "TTS stub：语音合成接口已就位，待接入云端服务",
	})
}
