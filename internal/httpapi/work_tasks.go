package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

func (s *Server) requireWorkTasks(w http.ResponseWriter) bool {
	if s.Tasks == nil || s.Artifacts == nil {
		writeError(w, http.StatusServiceUnavailable, "WorkTask 未启用")
		return false
	}
	return true
}

func writeWorkTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, worktask.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, worktask.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "任务不存在")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// POST /api/work-tasks
func (s *Server) handleCreateWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Title              string `json:"title"`
		Goal               string `json:"goal"`
		CompletionCriteria string `json:"completion_criteria"`
		Priority           int    `json:"priority"`
		Profile            string `json:"profile"`
		SourceTriggerID    string `json:"source_trigger_id"`
		ThreadID           string `json:"thread_id"`
		IdempotencyKey     string `json:"idempotency_key"`
		Status             string `json:"status"`
		Start              *bool  `json:"start"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	start := true
	if req.Start != nil {
		start = *req.Start
	}
	task, err := s.Tasks.Create(r.Context(), worktask.CreateInput{
		OwnerID: u.ID, Title: req.Title, Goal: req.Goal,
		CompletionCriteria: req.CompletionCriteria, Priority: req.Priority,
		Profile: req.Profile, SourceTriggerID: req.SourceTriggerID, ThreadID: req.ThreadID,
		IdempotencyKey: req.IdempotencyKey, Status: req.Status, Start: start, CreatedBy: u.ID,
	})
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": task})
}

// GET /api/work-tasks?status=&limit=
func (s *Server) handleListWorkTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.Tasks.List(r.Context(), u.ID, strings.TrimSpace(r.URL.Query().Get("status")),
		parseLimit(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	if items == nil {
		items = []store.WorkTask{}
	}
	writeJSON(w, 200, map[string]any{"tasks": items})
}

func (s *Server) handleGetWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	detail, err := s.Tasks.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, detail)
}

// POST /api/work-tasks/{id}/messages
func (s *Server) handleWorkTaskMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Text            string `json:"text"`
		ExpectedVersion *int   `json:"expected_version"`
		IdempotencyKey  string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	result, err := s.Tasks.AddMessage(r.Context(), u.ID, r.PathValue("id"), req.Text,
		req.ExpectedVersion, req.IdempotencyKey)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

type workTaskActionRequest struct {
	ExpectedVersion *int `json:"expected_version"`
}

func decodeTaskAction(w http.ResponseWriter, r *http.Request) (int, bool) {
	var req workTaskActionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, 400, "bad request body")
			return 0, false
		}
	}
	if req.ExpectedVersion == nil {
		return -1, true
	}
	return *req.ExpectedVersion, true
}

func (s *Server) handlePauseWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	task, err := s.Tasks.Pause(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"task": task})
}

func (s *Server) handleResumeWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	result, err := s.Tasks.Resume(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleCancelWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	task, err := s.Tasks.Cancel(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"task": task})
}

func (s *Server) handleRecoverWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	result, err := s.Tasks.Recover(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleReopenWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	task, err := s.Tasks.Reopen(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"task": task})
}

func (s *Server) handleCompleteWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	task, err := s.Tasks.Complete(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"task": task})
}

// GET /api/work-tasks/{id}/events?after_id=
func (s *Server) handleWorkTaskEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	taskID := r.PathValue("id")
	if _, err := s.Repos.WorkTasks.Get(r.Context(), u.ID, taskID); err != nil {
		writeWorkTaskError(w, err)
		return
	}
	after := parseEventID(r.URL.Query().Get("after_id"))
	events, err := s.Repos.AgentEvents.AfterForEntity(r.Context(), u.ID, "work_task", taskID,
		after, parseLimit(r.URL.Query().Get("limit"), 200))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if events == nil {
		events = []store.AgentEvent{}
	}
	next := after
	for _, event := range events {
		next = event.ID
	}
	writeJSON(w, 200, map[string]any{"events": events, "next_event_id": next})
}

func (s *Server) handleAttachWorkTaskThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Relation string `json:"relation"`
		Reason   string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, 400, "bad request body")
			return
		}
	}
	if err := s.Tasks.AddThread(r.Context(), u.ID, r.PathValue("id"), r.PathValue("thread_id"),
		req.Relation, req.Reason); err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 201, map[string]bool{"attached": true})
}

func (s *Server) handleDetachWorkTaskThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	relation := strings.TrimSpace(r.URL.Query().Get("relation"))
	if relation == "" {
		relation = store.WorkTaskRelationRelated
	}
	removed, err := s.Tasks.RemoveThread(r.Context(), u.ID, r.PathValue("id"), r.PathValue("thread_id"), relation)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	if !removed {
		writeError(w, 404, "关联不存在")
		return
	}
	writeJSON(w, 200, map[string]bool{"removed": true})
}

func (s *Server) handleListTaskArtifacts(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	detail, err := s.Tasks.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"artifacts": detail.Artifacts})
}

func (s *Server) handleCreateArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	task, err := s.Repos.WorkTasks.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	var req struct {
		Kind       string   `json:"kind"`
		Title      string   `json:"title"`
		Content    string   `json:"content"`
		SourceRefs []string `json:"source_refs"`
		JobID      string   `json:"job_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	jobID := req.JobID
	if jobID == "" {
		jobID = task.CurrentJobID
	}
	artifact, version, err := s.Artifacts.CreateDraft(r.Context(), worktask.CreateArtifactInput{
		OwnerID: u.ID, TaskID: task.ID, Kind: req.Kind, Title: req.Title,
		Content: req.Content, SourceRefs: req.SourceRefs, JobID: jobID,
	})
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"artifact": artifact, "version": version})
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	artifact, versions, err := s.Artifacts.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"artifact": artifact, "versions": versions})
}

func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	versionNo, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || versionNo <= 0 {
		writeError(w, 400, "version 无效")
		return
	}
	item, err := s.Artifacts.GetVersion(r.Context(), u.ID, r.PathValue("id"), versionNo)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	filename := "artifact-" + r.PathValue("id") + "-v" + strconv.Itoa(versionNo) + ".txt"
	if item.StorageKind == store.ArtifactStorageFile && item.OriginalName != "" {
		filename = item.OriginalName
	}
	reader, meta, err := s.Artifacts.OpenVersion(r.Context(), u.ID, r.PathValue("id"), versionNo)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", firstNonEmpty(meta.MimeType, "application/octet-stream"))
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.Header().Set("X-Content-Hash", meta.ContentHash)
	http.ServeContent(w, r, filename, meta.CreatedAt, reader)
}

// contentDisposition 同时提供 ASCII 回退文件名与 RFC 5987 UTF-8 文件名。
func contentDisposition(filename string) string {
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	var ascii strings.Builder
	for _, r := range filename {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			ascii.WriteByte('_')
			continue
		}
		ascii.WriteRune(r)
	}
	fallback := ascii.String()
	if fallback == "" || fallback == "." {
		fallback = "download"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		fallback, url.PathEscape(filename))
}

func (s *Server) handleDiffArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	to, _ := strconv.Atoi(r.URL.Query().Get("to"))
	if to <= 0 {
		artifact, versions, err := s.Artifacts.Get(r.Context(), u.ID, r.PathValue("id"))
		if err != nil {
			writeWorkTaskError(w, err)
			return
		}
		to = artifact.CurrentVersion
		if from <= 0 && len(versions) > 1 {
			from = versions[1].Version
		}
	}
	if from <= 0 {
		from = to - 1
	}
	if from <= 0 || from == to {
		writeError(w, 400, "from/to 无效")
		return
	}
	text, err := s.Artifacts.Diff(r.Context(), u.ID, r.PathValue("id"), from, to)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"from": from, "to": to, "diff": text})
}

func (s *Server) handleSubmitArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	artifact, err := s.Artifacts.SubmitForAcceptance(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"artifact": artifact})
}

func (s *Server) handleAcceptArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	version, ok := decodeTaskAction(w, r)
	if !ok {
		return
	}
	artifact, err := s.Artifacts.Accept(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"artifact": artifact})
}

func (s *Server) handleRejectArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		ExpectedVersion *int   `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	version := -1
	if req.ExpectedVersion != nil {
		version = *req.ExpectedVersion
	}
	artifact, err := s.Artifacts.Reject(r.Context(), u.ID, r.PathValue("id"), version, req.Reason)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"artifact": artifact})
}

// POST /api/artifacts/{id}/deliver
//
// 标记交付物已交付，并返回手机需要的下载信息（文件名、大小、校验和、桌面下载地址）。
// 「已交付」与「已验收」是两种不同动作，不能互相顶替。
func (s *Server) handleDeliverArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		ExpectedVersion *int `json:"expected_version"`
		Version         int  `json:"version"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, 400, "bad request body")
			return
		}
	}
	version := -1
	if req.ExpectedVersion != nil {
		version = *req.ExpectedVersion
	}
	artifact, err := s.Artifacts.MarkDelivered(r.Context(), u.ID, r.PathValue("id"), version)
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	info := map[string]any{"artifact": artifact}
	if req.Version > 0 {
		item, err := s.Artifacts.GetVersion(r.Context(), u.ID, artifact.ID, req.Version)
		if err != nil {
			writeWorkTaskError(w, err)
			return
		}
		info["version"] = item.Version
		info["name"] = item.OriginalName
		info["size_bytes"] = item.SizeBytes
		info["sha256"] = item.ContentHash
		info["mime_type"] = item.MimeType
		info["download_path"] = "/api/artifacts/" + artifact.ID +
			"/versions/" + strconv.Itoa(item.Version) + "/download"
	}
	writeJSON(w, 200, info)
}

func (s *Server) handleListTaskGrants(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	detail, err := s.Tasks.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"grants": detail.Grants})
}

func (s *Server) handleCreateTaskGrant(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	task, err := s.Repos.WorkTasks.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeWorkTaskError(w, err)
		return
	}
	var req struct {
		ActionType    string          `json:"action_type"`
		ResourceScope json.RawMessage `json:"resource_scope"`
		RiskLevel     string          `json:"risk_level"`
		RemainingUses int             `json:"remaining_uses"`
		ExpiresIn     int             `json:"expires_in_seconds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.ActionType = strings.TrimSpace(req.ActionType)
	if req.ActionType == "" {
		writeError(w, 400, "action_type 不能为空")
		return
	}
	switch req.RiskLevel {
	case "", store.GrantRead:
		req.RiskLevel = store.GrantRead
	case store.GrantPrepare, store.GrantExternalWrite, store.GrantSensitive:
	default:
		writeError(w, 400, "risk_level 无效")
		return
	}
	now := time.Now()
	grant := &store.ActionGrant{
		OwnerUserID: u.ID, TaskID: task.ID, ActionType: req.ActionType,
		ResourceScope: req.ResourceScope, RiskLevel: req.RiskLevel,
		RemainingUses: req.RemainingUses, ApprovedBy: u.ID, ApprovedAt: &now,
	}
	if req.RemainingUses == 0 {
		grant.RemainingUses = 1
	}
	if req.ExpiresIn > 0 {
		expires := now.Add(time.Duration(req.ExpiresIn) * time.Second)
		grant.ExpiresAt = &expires
	}
	if err := s.Repos.Grants.Create(r.Context(), grant); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"grant": grant})
}

func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkTasks(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	changed, err := s.Repos.Grants.Revoke(r.Context(), u.ID, r.PathValue("id"), time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 404, "授权不存在或已失效")
		return
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
}
