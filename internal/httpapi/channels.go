package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/superagent"
)

type activityEpisodeRequest struct {
	EpisodeID   string    `json:"episode_id"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	App         string    `json:"app"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	Status      string    `json:"status"`
	Outputs     []string  `json:"outputs"`
	Decisions   string    `json:"decisions"`
	Issues      string    `json:"issues"`
	EvidenceIDs []string  `json:"evidence_ids"`
	Cursor      string    `json:"cursor"`
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
	if req.EpisodeID == "" || req.Summary == "" {
		writeError(w, http.StatusBadRequest, "episode_id 和 summary 不能为空")
		return
	}
	if req.EndedAt.IsZero() {
		req.EndedAt = s.Now().In(time.Local)
	}
	day := req.EndedAt.In(time.Local).Format("2006-01-02")
	payload, _ := json.Marshal(req)
	result, err := s.SuperAgent.Ingest(r.Context(), u.ID, superagent.TriggerInput{
		Channel:          "activity",
		ExternalID:       "episode:" + req.EpisodeID,
		ThreadExternalID: "activity:" + day,
		Kind:             "episode",
		Title:            firstNonEmpty(req.Title, req.App, "工作片段"),
		Body:             req.Summary,
		Payload:          payload,
		ReceivedAt:       req.EndedAt,
	})
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
	s.ingestChannel(w, r, u.ID, "email", req.Account, superagent.TriggerInput{
		Channel:          "email",
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
	Source     string    `json:"source"`
	MessageID  string    `json:"message_id"`
	Sender     string    `json:"sender"`
	Text       string    `json:"text"`
	Kind       string    `json:"kind"`
	ReceivedAt time.Time `json:"received_at"`
	Incomplete bool      `json:"incomplete"`
	RawRef     string    `json:"raw_ref"`
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
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.Text = strings.TrimSpace(req.Text)
	if req.Source == "" || req.MessageID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "source、message_id、text 不能为空")
		return
	}
	if req.ReceivedAt.IsZero() {
		req.ReceivedAt = s.Now()
	}
	if req.Kind == "" {
		req.Kind = "message"
	}
	body := req.Text
	if req.Incomplete {
		body += "\n（可能不完整：仅采集到当前可见内容）"
	}
	payload, _ := json.Marshal(map[string]any{
		"sender": req.Sender, "incomplete": req.Incomplete, "raw_ref": req.RawRef,
	})
	s.ingestChannel(w, r, u.ID, "wechat", req.Source, superagent.TriggerInput{
		Channel:          "wechat",
		ExternalID:       "wechat:" + req.Source + ":" + req.MessageID,
		ThreadExternalID: "wechat:" + req.Source,
		Kind:             req.Kind,
		Title:            req.Source,
		Body:             body,
		Payload:          payload,
		ReceivedAt:       req.ReceivedAt,
	})
}

func (s *Server) ingestChannel(w http.ResponseWriter, r *http.Request, ownerID, channel, source string, input superagent.TriggerInput) {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"policies": policies, "skips_today": skips, "today": day,
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
	req.Channel = strings.TrimSpace(req.Channel)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
