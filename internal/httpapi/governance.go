package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// GET /api/memory/longterm?status=&limit=
func (s *Server) handleListLongTermEdges(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	edges, err := s.Repos.LongTerm.Edges(r.Context(), u.ID, parseLimit(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if status != "" {
		filtered := make([]store.LongTermEdge, 0, len(edges))
		for _, edge := range edges {
			if edge.Status == status {
				filtered = append(filtered, edge)
			}
		}
		edges = filtered
	}
	writeJSON(w, 200, map[string]any{"facts": edges})
}

func (s *Server) handleConfirmLongTermEdge(w http.ResponseWriter, r *http.Request) {
	s.setLongTermStatus(w, r, store.MemoryConfirmed)
}

func (s *Server) handleConflictLongTermEdge(w http.ResponseWriter, r *http.Request) {
	s.setLongTermStatus(w, r, store.MemoryConflicted)
}

func (s *Server) setLongTermStatus(w http.ResponseWriter, r *http.Request, status string) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	edge, err := s.Repos.LongTerm.GetEdge(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "事实不存在")
		return
	}
	changed, err := s.Repos.LongTerm.SetEdgeStatus(r.Context(), u.ID, edge.ID, status,
		req.ExpectedVersion, u.ID, time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 409, "事实版本已变化，请刷新后重试")
		return
	}
	_, _ = s.Repos.AgentEvents.Append(r.Context(), &store.AgentEvent{
		OwnerUserID: u.ID, Kind: "memory." + status, EntityType: "mem_edge", EntityID: edge.ID,
		Payload: json.RawMessage(`{}`),
	})
	writeJSON(w, 200, map[string]any{"ok": true, "status": status})
}

func (s *Server) handleListPersonalPolicies(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.Repos.PersonalPolicies.ListAll(r.Context(), u.ID, 200)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	scopeType := strings.TrimSpace(r.URL.Query().Get("scope_type"))
	scopeID := strings.TrimSpace(r.URL.Query().Get("scope_id"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	filtered := make([]store.PersonalPolicy, 0, len(items))
	for _, item := range items {
		if scopeType != "" && item.ScopeType != scopeType {
			continue
		}
		if scopeID != "" && item.ScopeID != scopeID {
			continue
		}
		if status != "" && item.Status != status {
			continue
		}
		filtered = append(filtered, item)
	}
	writeJSON(w, 200, map[string]any{"policies": filtered})
}

func (s *Server) handleCreatePersonalPolicy(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		PolicyType string          `json:"policy_type"`
		Name       string          `json:"name"`
		Content    json.RawMessage `json:"content"`
		ScopeType  string          `json:"scope_type"`
		ScopeID    string          `json:"scope_id"`
		Status     string          `json:"status"`
		ValidDays  int             `json:"valid_days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.PolicyType = strings.TrimSpace(req.PolicyType)
	req.Name = strings.TrimSpace(req.Name)
	switch req.PolicyType {
	case "focus", "preference", "boundary", "sop", "notification":
	default:
		writeError(w, 400, "policy_type 无效")
		return
	}
	if req.Name == "" {
		writeError(w, 400, "name 不能为空")
		return
	}
	switch req.ScopeType {
	case "", "global":
		req.ScopeType = "global"
	case "task", "channel", "device":
		if strings.TrimSpace(req.ScopeID) == "" {
			writeError(w, 400, "scope_id 不能为空")
			return
		}
	default:
		writeError(w, 400, "scope_type 无效")
		return
	}
	if req.Status == "" {
		req.Status = store.PolicyDraft
	}
	if req.Status != store.PolicyDraft && req.Status != store.PolicyActive && req.Status != store.PolicyDisabled {
		writeError(w, 400, "status 无效")
		return
	}
	policy := &store.PersonalPolicy{
		OwnerUserID: u.ID, PolicyType: req.PolicyType, Name: req.Name, Content: req.Content,
		ScopeType: req.ScopeType, ScopeID: req.ScopeID, Status: req.Status,
	}
	if req.ValidDays > 0 {
		to := time.Now().Add(time.Duration(req.ValidDays) * 24 * time.Hour)
		policy.ValidTo = &to
	}
	if err := s.Repos.PersonalPolicies.Create(r.Context(), policy); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"policy": policy})
}

func (s *Server) handlePersonalPolicyStatus(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Status          string `json:"status"`
		ExpectedVersion int    `json:"expected_version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	if req.Status != store.PolicyActive && req.Status != store.PolicyDisabled && req.Status != store.PolicyDraft {
		writeError(w, 400, "status 无效")
		return
	}
	changed, err := s.Repos.PersonalPolicies.SetStatus(r.Context(), u.ID, r.PathValue("id"),
		req.ExpectedVersion, req.Status, time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 409, "策略版本已变化，请刷新后重试")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": req.Status})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	devices, err := s.Repos.Devices.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"devices": devices})
}

func (s *Server) handleUpsertDevice(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		DeviceType string `json:"device_type"`
		Online     bool   `json:"online"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	req.DeviceType = strings.TrimSpace(req.DeviceType)
	if req.ID == "" || req.Name == "" {
		writeError(w, 400, "id 和 name 不能为空")
		return
	}
	now := time.Now()
	device := &store.DeviceEnvironment{
		ID: req.ID, OwnerUserID: u.ID, Name: req.Name, DeviceType: req.DeviceType,
		Online: req.Online, LastSeenAt: &now,
	}
	if err := s.Repos.Devices.Upsert(r.Context(), device); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"device": device})
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	changed, err := s.Repos.Devices.Revoke(r.Context(), u.ID, r.PathValue("id"), time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 404, "设备不存在或已撤销")
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true})
}

func (s *Server) handleListDeviceCapabilities(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	if _, err := s.Repos.Devices.Get(r.Context(), u.ID, r.PathValue("id")); err != nil {
		writeError(w, 404, "设备不存在")
		return
	}
	items, err := s.Repos.Devices.Capabilities(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"capabilities": items})
}

func (s *Server) handleAddDeviceCapability(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	device, err := s.Repos.Devices.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "设备不存在")
		return
	}
	if device.RevokedAt != nil {
		writeError(w, 409, "设备已撤销")
		return
	}
	var req struct {
		ToolName string          `json:"tool_name"`
		Resource json.RawMessage `json:"resource_scope"`
		WorkDir  string          `json:"work_dir"`
		Limits   json.RawMessage `json:"limits"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.ToolName = strings.TrimSpace(req.ToolName)
	if req.ToolName == "" {
		writeError(w, 400, "tool_name 不能为空")
		return
	}
	capability := &store.DeviceCapability{
		OwnerUserID: u.ID, DeviceID: device.ID, ToolName: req.ToolName,
		Resource: req.Resource, WorkDir: strings.TrimSpace(req.WorkDir), Limits: req.Limits,
	}
	if err := s.Repos.Devices.AddCapability(r.Context(), capability); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"capability": capability})
}

func (s *Server) handleBindTaskDevice(w http.ResponseWriter, r *http.Request) {
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
	device, err := s.Repos.Devices.Get(r.Context(), u.ID, r.PathValue("device_id"))
	if err != nil {
		writeError(w, 404, "设备不存在")
		return
	}
	if device.RevokedAt != nil {
		writeError(w, 409, "设备已撤销")
		return
	}
	var req struct {
		CapabilityID string `json:"capability_id"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, 400, "bad request body")
			return
		}
	}
	if req.CapabilityID != "" {
		caps, err := s.Repos.Devices.Capabilities(r.Context(), u.ID, device.ID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		found := false
		for _, cap := range caps {
			if cap.ID == req.CapabilityID {
				found = true
				break
			}
		}
		if !found {
			writeError(w, 400, "capability_id 不属于该设备")
			return
		}
	}
	binding := &store.TaskDeviceBinding{
		OwnerUserID: u.ID, TaskID: task.ID, DeviceID: device.ID,
		CapabilityID: strings.TrimSpace(req.CapabilityID),
	}
	if err := s.Repos.Devices.BindTask(r.Context(), binding); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"binding": binding})
}
