package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/voice"
)

// 语音口令判定端点：手机只做「识别音频 + 执行动作」，
// 唤醒词、指令窗口、片段合并、上下文窗口的判定都在 Go（internal/voice）。

type voiceTextRequest struct {
	// DeviceID 区分听写来源，默认单设备。
	DeviceID string `json:"device_id"`
	Text     string `json:"text"`
	// Flush 表示「等待后文超时了，请把暂存片段收尾」。
	Flush bool `json:"flush"`
	// CapturedAt 可回填片段的真实说话时间（默认当前时间）。
	CapturedAt *time.Time `json:"captured_at"`
}

type voiceTextResponse struct {
	Actions []voice.Action `json:"actions"`
	// NextFlushMs 告诉手机多久没有后文就该发 flush。
	NextFlushMs int64 `json:"next_flush_ms"`
	// Captured 是本次真正写入情景记忆的片段 id（无写入时为空）。
	Captured []string `json:"captured,omitempty"`
}

// handleVoiceText 处理最终转写文本，返回手机要执行的动作，并落库需要记忆的片段。
func (s *Server) handleVoiceText(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	if s.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "voice 服务未启用")
		return
	}
	var req voiceTextRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	var actions []voice.Action
	switch {
	case req.Flush:
		actions = s.Voice.Flush(req.DeviceID)
	case req.Text != "":
		actions = s.Voice.OnText(req.DeviceID, req.Text)
	default:
		writeError(w, 400, "需要 text 或 flush")
		return
	}

	resp := voiceTextResponse{
		Actions:     actions,
		NextFlushMs: s.Voice.MergeWait().Milliseconds(),
	}
	captured, err := s.captureVoiceActions(r, u.ID, actions, req.CapturedAt)
	if err != nil {
		// 片段已判定出来但写库失败：如实回报，别让用户以为记住了。
		writeError(w, 500, err.Error())
		return
	}
	resp.Captured = captured
	writeJSON(w, 200, resp)
}

// captureVoiceActions 把 capture 动作写进情景记忆；speak/chat 由手机执行。
func (s *Server) captureVoiceActions(r *http.Request, ownerID string, actions []voice.Action, at *time.Time) ([]string, error) {
	var pending []voice.Action
	for _, a := range actions {
		if a.Type == voice.ActionCapture {
			pending = append(pending, a)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	if s.Runtime == nil || s.Runtime.Tools == nil || s.Runtime.Tools.Memory == nil {
		return nil, errors.New("记忆服务不可用，片段未保存")
	}
	svc := s.Runtime.Tools.Memory
	var ids []string
	for _, a := range pending {
		capturedAt := a.At
		if at != nil {
			capturedAt = *at
		}
		result, err := svc.CaptureEpisode(r.Context(), memory.CaptureInput{
			OwnerUserID: ownerID, Text: a.Text,
			ContextBefore: a.Before, ContextAfter: a.After,
			Source: "android_speech", CapturedAt: capturedAt,
		})
		if err != nil {
			return ids, err
		}
		if result != nil && result.Episode != nil {
			ids = append(ids, result.Episode.ID)
		}
	}
	return ids, nil
}
