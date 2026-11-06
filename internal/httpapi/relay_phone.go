package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"goldenfinger/agent/internal/relay"
)

// PhoneRelay 是手机端到电脑的中继通道（生活管家发起，电脑执行）。
type PhoneRelay interface {
	Pair(ctx context.Context, baseURL, code, phoneName string) (relay.PhoneState, error)
	Status() map[string]any
	SendWithID(ctx context.Context, id string, payload any) (relay.ResultPayload, error)
	SubmitJob(ctx context.Context, req relay.JobRequest) (relay.ResultPayload, error)
	JobStatus(ctx context.Context, taskID string) (relay.ResultPayload, error)
	CancelJob(ctx context.Context, taskID string) (relay.ResultPayload, error)
	Unpair() error
}

func (s *Server) requirePhoneRelay(w http.ResponseWriter) bool {
	if s.PhoneRelay == nil {
		writeError(w, 503, "手机远程未启用")
		return false
	}
	return true
}

type phoneSendRequest struct {
	Text    string `json:"text"`
	AgentID string `json:"agent_id"`
	WorkDir string `json:"work_dir"`
	// ClientID 是手机端生成的幂等键。超时重试必须复用同一个值，
	// 电脑端据此保证「最多执行一次」。
	ClientID string `json:"client_id"`
}

// POST /api/relay/phone/pair — 输入电脑上显示的配对码完成绑定。
func (s *Server) handlePhonePair(w http.ResponseWriter, r *http.Request) {
	if !s.requirePhoneRelay(w) {
		return
	}
	var req struct {
		URL       string `json:"url"`
		Code      string `json:"code"`
		PhoneName string `json:"phone_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	state, err := s.PhoneRelay.Pair(r.Context(), req.URL, req.Code, req.PhoneName)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"paired": true, "state": state, "status": s.PhoneRelay.Status()})
}

// GET /api/relay/phone/status
func (s *Server) handlePhoneStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requirePhoneRelay(w) {
		return
	}
	writeJSON(w, 200, s.PhoneRelay.Status())
}

// DELETE /api/relay/phone/pair — 清除本机配对（电脑端仍可单独解除绑定）。
func (s *Server) handlePhoneUnpair(w http.ResponseWriter, r *http.Request) {
	if !s.requirePhoneRelay(w) {
		return
	}
	if err := s.PhoneRelay.Unpair(); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "unpaired"})
}

// POST /api/relay/phone/send — 让电脑执行一句话，返回电脑的结果。
//
// 电脑离线时返回 202 + queued=true：指令已在中继排队，
// 不能谎称已执行，也不该让用户以为失败。
func (s *Server) handlePhoneSend(w http.ResponseWriter, r *http.Request) {
	if !s.requirePhoneRelay(w) {
		return
	}
	var req phoneSendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	payload := map[string]any{"text": req.Text}
	if req.AgentID != "" {
		payload["agent_id"] = req.AgentID
	}
	if req.WorkDir != "" {
		payload["work_dir"] = req.WorkDir
	}
	commandID := req.ClientID
	if commandID == "" {
		commandID = newCommandID()
	}
	result, err := s.PhoneRelay.SendWithID(r.Context(), commandID, payload)
	if err != nil {
		// 电脑离线：指令已进中继队列，如实告诉用户“排队中”，不伪装成成功或失败。
		if errors.Is(err, relay.ErrDesktopOffline) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"queued": true, "reply": "指令已排队，等电脑上线后执行", "command_id": commandID,
			})
			return
		}
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": result.OK, "reply": result.Reply, "desktop_error": result.Error, "command_id": commandID,
	})
}

// newCommandID 为手机指令生成幂等键（失败重试时由客户端复用）。
func newCommandID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	}
	return "cmd-" + hex.EncodeToString(buf)
}

type phoneJobRequest struct {
	Text     string `json:"text"`
	AgentID  string `json:"agent_id"`
	WorkDir  string `json:"work_dir"`
	ClientID string `json:"client_id"`
}

// POST /api/relay/phone/job —— 让电脑在后台执行长任务，立刻返回 task_id。
func (s *Server) handlePhoneJobSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.requirePhoneRelay(w) {
		return
	}
	var req phoneJobRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	if req.ClientID == "" {
		req.ClientID = newCommandID()
	}
	result, err := s.PhoneRelay.SubmitJob(r.Context(), relay.JobRequest{
		Text: req.Text, AgentID: req.AgentID, WorkDir: req.WorkDir, ClientID: req.ClientID,
	})
	if err != nil {
		if errors.Is(err, relay.ErrDesktopOffline) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"queued": true, "reply": "电脑离线，指令已排队", "client_id": req.ClientID,
			})
			return
		}
		writeError(w, 502, err.Error())
		return
	}
	if !result.OK {
		writeError(w, 502, result.Error)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": result.TaskID, "job_id": result.JobID, "work_task_id": result.WorkTaskID,
		"status": result.Status, "reply": result.Reply, "client_id": req.ClientID,
	})
}

// GET /api/relay/phone/job/{id} —— 查询电脑上的任务进度（断线重连也能接着看）。
func (s *Server) handlePhoneJobStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requirePhoneRelay(w) {
		return
	}
	result, err := s.PhoneRelay.JobStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"task_id": result.TaskID, "job_id": result.JobID, "work_task_id": result.WorkTaskID,
		"status": result.Status, "reply": result.Reply,
		"desktop_error": result.Error, "events": result.Events,
	})
}

// DELETE /api/relay/phone/job/{id} —— 取消电脑上仍在排队/执行的任务。
func (s *Server) handlePhoneJobCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requirePhoneRelay(w) {
		return
	}
	result, err := s.PhoneRelay.CancelJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	if !result.OK {
		writeError(w, 409, result.Error)
		return
	}
	writeJSON(w, 200, map[string]any{"task_id": result.TaskID, "status": result.Status, "reply": result.Reply})
}
