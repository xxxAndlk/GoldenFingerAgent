package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

type activityEpisodeRequest struct {
	EpisodeID             string    `json:"episode_id"`
	ContextKey            string    `json:"context_key"`
	StartedAt             time.Time `json:"started_at"`
	EndedAt               time.Time `json:"ended_at"`
	App                   string    `json:"app"`
	Apps                  []string  `json:"apps"`
	ProjectCandidates     []string  `json:"project_candidates"`
	Title                 string    `json:"title"`
	Summary               string    `json:"summary"`
	Status                string    `json:"status"`
	Outputs               []string  `json:"outputs"`
	Decisions             string    `json:"decisions"`
	Issues                string    `json:"issues"`
	EvidenceIDs           []string  `json:"evidence_ids"`
	Cursor                string    `json:"cursor"`
	AssociationConfidence float64   `json:"association_confidence"`
}

// POST /api/channels/activity/episodes —— 桌面端把已完成的工作片段交给事件层。
func (s *Server) handleActivityEpisode(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil || !s.requireSuperAgent(w) {
		return
	}
	var req activityEpisodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.EpisodeID = strings.TrimSpace(req.EpisodeID)
	req.Summary = strings.TrimSpace(req.Summary)
	req.ContextKey = strings.TrimSpace(req.ContextKey)
	if len(req.ContextKey) > 300 {
		req.ContextKey = req.ContextKey[:300]
	}
	if req.EpisodeID == "" || req.Summary == "" {
		writeError(w, http.StatusBadRequest, "episode_id 和 summary 不能为空")
		return
	}
	if req.EndedAt.IsZero() {
		req.EndedAt = s.Now().In(time.Local)
	}
	day := req.EndedAt.In(time.Local).Format("2006-01-02")
	threadKey := "activity:" + day
	if req.ContextKey != "" {
		threadKey = req.ContextKey
		if !strings.HasPrefix(threadKey, "activity:") {
			threadKey = "activity:" + threadKey
		}
	}
	payload, _ := json.Marshal(req)
	s.ingestChannel(w, r, u.ID, store.ChannelActivity, firstNonEmpty(req.App, "activity"), superagent.TriggerInput{
		Channel:          store.ChannelActivity,
		ExternalID:       "episode:" + req.EpisodeID,
		ThreadExternalID: threadKey,
		Kind:             "episode",
		Title:            firstNonEmpty(req.Title, req.App, "工作片段"),
		Body:             req.Summary,
		Payload:          payload,
		ReceivedAt:       req.EndedAt,
	})
}

type emailMessageRequest struct {
	Account     string          `json:"account"`
	Mailbox     string          `json:"mailbox"`
	MessageID   string          `json:"message_id"`
	ThreadKey   string          `json:"thread_key"`
	From        string          `json:"from"`
	To          []string        `json:"to"`
	Cc          []string        `json:"cc"`
	Subject     string          `json:"subject"`
	Text        string          `json:"text"`
	Snippet     string          `json:"snippet"`
	ReceivedAt  time.Time       `json:"received_at"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
	RawRef      string          `json:"raw_ref"`
}

func (s *Server) handleEmailMessage(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil || !s.requireSuperAgent(w) {
		return
	}
	var req emailMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.Account = strings.TrimSpace(req.Account)
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.ThreadKey = strings.TrimSpace(req.ThreadKey)
	req.Subject = strings.TrimSpace(req.Subject)
	req.Text = strings.TrimSpace(req.Text)
	if req.MessageID == "" || (req.Subject == "" && req.Text == "") {
		writeError(w, http.StatusBadRequest, "message_id 和邮件内容不能为空")
		return
	}
	if req.ReceivedAt.IsZero() {
		req.ReceivedAt = s.Now()
	}
	threadKey := req.ThreadKey
	if threadKey == "" {
		threadKey = req.MessageID
	}
	payload, _ := json.Marshal(map[string]any{
		"account": req.Account, "mailbox": req.Mailbox, "from": req.From,
		"to": req.To, "cc": req.Cc, "snippet": req.Snippet,
		"attachments": req.Attachments, "raw_ref": req.RawRef,
	})
	s.ingestChannel(w, r, u.ID, store.ChannelEmail, req.Account, superagent.TriggerInput{
		Channel:          store.ChannelEmail,
		ExternalID:       "email:" + req.Account + ":" + req.MessageID,
		ThreadExternalID: "email:" + req.Account + ":" + threadKey,
		Kind:             "email_message",
		Title:            firstNonEmpty(req.Subject, req.Snippet, "邮件"),
		Body:             firstNonEmpty(req.Text, req.Snippet),
		Payload:          payload,
		ReceivedAt:       req.ReceivedAt,
	})
}

type calendarEventRequest struct {
	Calendar    string    `json:"calendar"`
	EventID     string    `json:"event_id"`
	SeriesID    string    `json:"series_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Location    string    `json:"location"`
	StartAt     time.Time `json:"start_at"`
	EndAt       time.Time `json:"end_at"`
	AllDay      bool      `json:"all_day"`
	Attendees   []string  `json:"attendees"`
	Status      string    `json:"status"`
	RawRef      string    `json:"raw_ref"`
}

func (s *Server) handleCalendarEvent(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil || !s.requireSuperAgent(w) {
		return
	}
	var req calendarEventRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.EventID = strings.TrimSpace(req.EventID)
	req.Title = strings.TrimSpace(req.Title)
	if req.EventID == "" || (req.Title == "" && req.Description == "") {
		writeError(w, http.StatusBadRequest, "event_id 和 title 不能为空")
		return
	}
	if req.Calendar == "" {
		req.Calendar = "default"
	}
	if req.Status == "" {
		req.Status = "confirmed"
	}
	threadKey := firstNonEmpty(req.SeriesID, req.EventID)
	payload, _ := json.Marshal(map[string]any{
		"calendar": req.Calendar, "event_id": req.EventID, "series_id": req.SeriesID,
		"start_at": req.StartAt, "end_at": req.EndAt, "all_day": req.AllDay,
		"attendees": req.Attendees, "status": req.Status, "raw_ref": req.RawRef,
	})
	body := req.Description
	if req.Location != "" {
		body = strings.TrimSpace(body + "\n地点：" + req.Location)
	}
	if !req.StartAt.IsZero() {
		body = strings.TrimSpace(body + "\n开始：" + req.StartAt.In(time.Local).Format("2006-01-02 15:04"))
	}
	s.ingestChannel(w, r, u.ID, "calendar", req.Calendar, superagent.TriggerInput{
		Channel: "calendar", Kind: "calendar_event",
		ExternalID:       "calendar:" + req.Calendar + ":" + req.EventID,
		ThreadExternalID: "calendar:" + req.Calendar + ":" + threadKey,
		Title:            firstNonEmpty(req.Title, "日历事件"),
		Body:             body,
		Payload:          payload,
		ReceivedAt:       s.Now(),
	})
}

type wechatMessageRequest struct {
	Source       string     `json:"source"`
	SourceID     string     `json:"source_id"`
	DisplayName  string     `json:"display_name"`
	MessageID    string     `json:"message_id"`
	EventID      string     `json:"event_id"`
	Sender       string     `json:"sender"`
	Direction    string     `json:"direction"`
	MessageType  string     `json:"message_type"`
	Text         string     `json:"text"`
	Kind         string     `json:"kind"`
	ObservedAt   time.Time  `json:"observed_at"`
	OccurredAt   *time.Time `json:"occurred_at"`
	ReceivedAt   time.Time  `json:"received_at"`
	ContentHash  string     `json:"content_hash"`
	Completeness string     `json:"completeness"`
	Incomplete   bool       `json:"incomplete"`
	RawRef       string     `json:"raw_ref"`
	Cursor       string     `json:"cursor"`
}

func (s *Server) handleWeChatMessage(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil || !s.requireSuperAgent(w) {
		return
	}
	var req wechatMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	req.SourceID = strings.TrimSpace(req.SourceID)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.EventID = strings.TrimSpace(req.EventID)
	req.Text = strings.TrimSpace(req.Text)
	req.Sender = strings.TrimSpace(req.Sender)
	req.Direction = strings.TrimSpace(req.Direction)
	req.ContentHash = strings.TrimSpace(req.ContentHash)
	req.Completeness = strings.TrimSpace(req.Completeness)
	if req.Source == "" && req.DisplayName != "" {
		req.Source = req.DisplayName
	}
	if req.MessageID == "" {
		req.MessageID = req.EventID
	}
	if req.Source == "" || req.MessageID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "source、message_id、text 不能为空")
		return
	}
	if req.SourceID == "" {
		req.SourceID = req.Source
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Source
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = req.ReceivedAt
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = s.Now()
	}
	if req.ReceivedAt.IsZero() {
		req.ReceivedAt = req.ObservedAt
	}
	if req.Kind == "" {
		req.Kind = "message"
	}
	if req.MessageType == "" {
		req.MessageType = "visible_text"
	}
	if !validDirection(req.Direction) {
		req.Direction = "unknown"
	}
	if !validCompleteness(req.Completeness) {
		req.Completeness = "unknown"
	}
	body := req.Text
	if req.Completeness == "current_visible" || req.Completeness == "partial" || req.Incomplete {
		body += "\n（当前可见内容，不代表完整聊天记录）"
	}
	payload, _ := json.Marshal(map[string]any{
		"sender": req.Sender, "direction": req.Direction, "message_type": req.MessageType,
		"source_id": req.SourceID, "display_name": req.DisplayName,
		"event_id": req.EventID, "content_hash": req.ContentHash,
		"observed_at": req.ObservedAt, "occurred_at": req.OccurredAt,
		"completeness": req.Completeness, "incomplete": req.Incomplete,
		"raw_ref": req.RawRef, "cursor": req.Cursor,
	})
	s.ingestChannel(w, r, u.ID, store.ChannelWeChatDesktop, req.Source, superagent.TriggerInput{
		Channel:          store.ChannelWeChatDesktop,
		ExternalID:       "wechat:" + req.SourceID + ":" + req.MessageID,
		ThreadExternalID: "wechat:" + req.SourceID,
		Kind:             req.Kind,
		Title:            req.DisplayName,
		Body:             body,
		Payload:          payload,
		ReceivedAt:       req.ObservedAt,
	})
}

func validDirection(value string) bool {
	switch value {
	case "self", "other", "system", "unknown":
		return true
	default:
		return false
	}
}

func validCompleteness(value string) bool {
	switch value {
	case "complete", "current_visible", "partial", "unknown":
		return true
	default:
		return false
	}
}

func (s *Server) ingestChannel(w http.ResponseWriter, r *http.Request, ownerID, channel, source string, input superagent.TriggerInput) {
	enabled, err := s.channelEnabled(r.Context(), ownerID, channel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !enabled {
		_ = s.Repos.ChannelSkips.Increment(r.Context(), ownerID, channel,
			s.Now().In(time.Local).Format("2006-01-02"), s.Now())
		writeJSON(w, http.StatusAccepted, map[string]any{"filtered": true, "reason": "channel disabled"})
		return
	}
	allowed, err := s.Repos.ChannelPolicies.Allowed(r.Context(), ownerID, channel, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !allowed {
		_ = s.Repos.ChannelSkips.Increment(r.Context(), ownerID, channel,
			s.Now().In(time.Local).Format("2006-01-02"), s.Now())
		writeJSON(w, http.StatusAccepted, map[string]any{"filtered": true, "reason": "channel source denied"})
		return
	}
	result, err := s.SuperAgent.Ingest(r.Context(), ownerID, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (s *Server) handleChannelSettings(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	policies, err := s.Repos.ChannelPolicies.List(r.Context(), u.ID, strings.TrimSpace(r.URL.Query().Get("channel")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	day := s.Now().In(time.Local).Format("2006-01-02")
	skips, err := s.Repos.ChannelSkips.List(r.Context(), u.ID, day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	configs, err := s.Repos.ChannelConfigs.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	global, err := s.Repos.GlobalConfigs.Get(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configs": configs, "global": global,
		"policies": policies, "skips_today": skips, "today": day,
		"channels":       store.GatedChannels,
		"wechat_default": "deny", "other_default": "allow",
	})
}

func (s *Server) handleUpdateChannelSettings(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Channel string `json:"channel"`
		Source  string `json:"source"`
		Allowed bool   `json:"allowed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.Channel = store.NormalizeChannel(strings.TrimSpace(req.Channel))
	req.Source = strings.TrimSpace(req.Source)
	if req.Channel == "" {
		writeError(w, http.StatusBadRequest, "channel 不能为空")
		return
	}
	if err := s.Repos.ChannelPolicies.Set(r.Context(), u.ID, req.Channel, req.Source, req.Allowed, s.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": true})
}

func (s *Server) handleUpdateChannelConfig(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	channel := store.NormalizeChannel(strings.TrimSpace(r.PathValue("channel")))
	if !store.IsGatedChannel(channel) {
		writeError(w, http.StatusBadRequest, "未知渠道")
		return
	}
	var req struct {
		Enabled           bool `json:"enabled"`
		SummaryEnabled    bool `json:"summary_enabled"`
		IndexEnabled      bool `json:"index_enabled"`
		BriefEnabled      bool `json:"brief_enabled"`
		AutoWorkerEnabled bool `json:"auto_worker_enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	item := store.ChannelConfig{
		OwnerUserID: u.ID, Channel: channel,
		Enabled: req.Enabled, SummaryEnabled: req.SummaryEnabled,
		IndexEnabled: req.IndexEnabled, BriefEnabled: req.BriefEnabled,
		AutoWorkerEnabled: req.AutoWorkerEnabled,
	}
	if err := s.Repos.ChannelConfigs.Save(r.Context(), item, s.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	item, err := s.Repos.ChannelConfigs.Get(r.Context(), u.ID, channel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleUpdateGlobalConfig(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		RouterEnabled          bool `json:"router_enabled"`
		WorkerEnabled          bool `json:"worker_enabled"`
		DailyBriefEnabled      bool `json:"daily_brief_enabled"`
		ProactiveNotifyEnabled bool `json:"proactive_notify_enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	item := store.GlobalConfig{
		OwnerUserID: u.ID, RouterEnabled: req.RouterEnabled, WorkerEnabled: req.WorkerEnabled,
		DailyBriefEnabled: req.DailyBriefEnabled, ProactiveNotifyEnabled: req.ProactiveNotifyEnabled,
	}
	if err := s.Repos.GlobalConfigs.Save(r.Context(), item, s.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	item, err := s.Repos.GlobalConfigs.Get(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) channelEnabled(ctx context.Context, ownerID, channel string) (bool, error) {
	channel = store.NormalizeChannel(channel)
	if !store.IsGatedChannel(channel) {
		return true, nil
	}
	if s.Repos == nil || s.Repos.ChannelConfigs == nil {
		return false, nil
	}
	return s.Repos.ChannelConfigs.Enabled(ctx, ownerID, channel)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
