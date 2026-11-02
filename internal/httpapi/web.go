package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

var webSiteIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (s *Server) requireSettings(w http.ResponseWriter) bool {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "设置未启用")
		return false
	}
	return true
}

// GET /api/settings/web/sites
func (s *Server) handleWebSites(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettings(w) {
		return
	}
	sites := s.Settings.Web().Sites
	if sites == nil {
		sites = []settings.WebSite{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sites": sites,
		"notes": "网页站点默认关闭；自动登录只接受系统安全存储（Windows DPAPI）中的凭据，不接受明文配置。",
	})
}

// PUT /api/settings/web/sites/{id}
func (s *Server) handlePutWebSite(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettings(w) {
		return
	}
	var req settings.WebSite
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.ID = strings.TrimSpace(r.PathValue("id"))
	site, err := normalizeWebSite(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Settings.UpsertSite(site); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, site)
}

// DELETE /api/settings/web/sites/{id}
func (s *Server) handleDeleteWebSite(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettings(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id 不能为空")
		return
	}
	if err := s.Settings.DeleteSite(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type webItemRequest struct {
	SiteID       string    `json:"site_id"`
	Account      string    `json:"account"`
	URL          string    `json:"url"`
	CanonicalURL string    `json:"canonical_url"`
	ItemID       string    `json:"item_id"`
	Title        string    `json:"title"`
	Text         string    `json:"text"`
	CapturedAt   time.Time `json:"captured_at"`
	ExtractedBy  string    `json:"extracted_by"`
	Completeness string    `json:"completeness"`
	EvidenceIDs  []string  `json:"evidence_ids"`
}

// POST /api/channels/web/items
func (s *Server) handleWebItem(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil || !s.requireSuperAgent(w) {
		return
	}
	if !s.requireSettings(w) {
		return
	}
	var req webItemRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	req.SiteID = strings.TrimSpace(req.SiteID)
	req.Account = strings.TrimSpace(req.Account)
	req.ItemID = strings.TrimSpace(req.ItemID)
	req.Title = strings.TrimSpace(req.Title)
	req.Text = strings.TrimSpace(req.Text)
	if req.SiteID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "site_id 和 text 不能为空")
		return
	}
	site, ok := s.Settings.Site(req.SiteID)
	if !ok {
		writeError(w, http.StatusNotFound, "站点未配置")
		return
	}
	if !site.Enabled {
		_ = s.Repos.ChannelSkips.Increment(r.Context(), u.ID, store.ChannelWeb,
			s.Now().In(time.Local).Format("2006-01-02"), s.Now())
		writeJSON(w, http.StatusAccepted, map[string]any{"filtered": true, "reason": "web site disabled"})
		return
	}
	account := firstNonEmpty(req.Account, site.Account)
	if site.Account != "" && req.Account != "" && req.Account != site.Account {
		writeError(w, http.StatusBadRequest, "account 与站点配置不一致")
		return
	}
	if account == "" {
		account = site.ID
	}
	canonicalRaw := firstNonEmpty(req.CanonicalURL, req.URL)
	if canonicalRaw == "" {
		writeError(w, http.StatusBadRequest, "url 或 canonical_url 必填")
		return
	}
	canonical, err := superagent.CheckWebURL(canonicalRaw, site.AllowedOrigins, site.AllowedPathPrefixes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL != "" {
		if _, err := superagent.CheckWebURL(req.URL, site.AllowedOrigins, site.AllowedPathPrefixes); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(superagent.NormalizeWebText(req.Text)) > 512<<10 {
		writeError(w, http.StatusBadRequest, "网页正文超过上限")
		return
	}
	itemID := firstNonEmpty(req.ItemID, canonical)
	contentHash := superagent.WebContentHash(req.Text)
	extractedBy := normalizeWebExtractedBy(req.ExtractedBy)
	completeness := normalizeWebCompleteness(req.Completeness)
	if req.CapturedAt.IsZero() {
		req.CapturedAt = s.Now()
	}
	payload, _ := json.Marshal(superagent.WebPayload{
		SiteID: site.ID, Account: account, URL: strings.TrimSpace(req.URL),
		CanonicalURL: canonical, ItemID: itemID, ContentHash: contentHash,
		ExtractedBy: extractedBy, Completeness: completeness,
		EvidenceIDs: normalizeItems(req.EvidenceIDs, 50, 200),
	})
	s.ingestChannel(w, r, u.ID, store.ChannelWeb, site.ID, superagent.TriggerInput{
		Channel:          store.ChannelWeb,
		ExternalID:       superagent.WebItemExternalID(site.ID, account, itemID, contentHash),
		ThreadExternalID: superagent.WebThreadExternalID(site.ID, account, canonical),
		Kind:             superagent.WebKind,
		Title:            firstNonEmpty(req.Title, itemID, "网页条目"),
		Body:             req.Text,
		Payload:          payload,
		ReceivedAt:       req.CapturedAt,
		Trusted:          false,
		PrivacyLevel:     "normal",
	})
}

func normalizeWebExtractedBy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "adapter", "readability", "dom", "ocr_fallback":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "dom"
	}
}

func normalizeWebCompleteness(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "complete", "partial", "unknown":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func normalizeItems(values []string, maxItems, maxLen int) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		if len([]rune(value)) > maxLen {
			value = string([]rune(value)[:maxLen])
		}
		seen[value] = true
		out = append(out, value)
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

func normalizeWebSite(site settings.WebSite) (settings.WebSite, error) {
	site.ID = strings.TrimSpace(site.ID)
	site.Account = strings.TrimSpace(site.Account)
	site.CredentialRef = strings.TrimSpace(site.CredentialRef)
	site.LoginURL = strings.TrimSpace(site.LoginURL)
	site.UsernameSelector = strings.TrimSpace(site.UsernameSelector)
	site.PasswordSelector = strings.TrimSpace(site.PasswordSelector)
	site.SubmitSelector = strings.TrimSpace(site.SubmitSelector)
	if !webSiteIDPattern.MatchString(site.ID) {
		return site, errInvalid("id 只能包含字母、数字、下划线或短横线，长度 1-80")
	}
	if site.CredentialRef != "" && !webSiteIDPattern.MatchString(site.CredentialRef) {
		return site, errInvalid("credential_ref 只能包含字母、数字、下划线或短横线，长度 1-80")
	}
	startURLs := make([]string, 0, len(site.StartURLs))
	derivedOrigins := make([]string, 0, len(site.StartURLs))
	for _, raw := range site.StartURLs {
		canonical, err := superagent.CanonicalWebURL(raw)
		if err != nil {
			return site, errInvalid("起始 URL 无效：" + err.Error())
		}
		origin, err := superagent.WebOrigin(canonical)
		if err != nil {
			return site, errInvalid("起始 URL 无效：" + err.Error())
		}
		startURLs = append(startURLs, canonical)
		derivedOrigins = appendUnique(derivedOrigins, origin)
	}
	if (site.Enabled || site.NavigationEnabled) && len(startURLs) == 0 {
		return site, errInvalid("启用采集或导航时至少需要一个起始 URL")
	}
	origins := make([]string, 0, len(site.AllowedOrigins))
	for _, raw := range site.AllowedOrigins {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return site, err
		}
		origins = appendUnique(origins, origin)
	}
	if len(origins) == 0 {
		origins = derivedOrigins
	}
	prefixes := make([]string, 0, len(site.AllowedPathPrefixes))
	for _, raw := range site.AllowedPathPrefixes {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "/") {
			raw = "/" + raw
		}
		prefixes = appendUnique(prefixes, raw)
	}
	if len(prefixes) == 0 {
		prefixes = []string{"/"}
	}
	for _, startURL := range startURLs {
		if _, err := superagent.CheckWebURL(startURL, origins, prefixes); err != nil {
			return site, errInvalid("起始 URL 不在授权范围：" + err.Error())
		}
	}
	if site.LoginURL != "" {
		loginURL, err := superagent.CheckWebURL(site.LoginURL, origins, prefixes)
		if err != nil {
			return site, errInvalid("登录 URL 不在授权范围：" + err.Error())
		}
		if !strings.HasPrefix(strings.ToLower(loginURL), "https://") {
			return site, errInvalid("自动登录只允许 HTTPS 登录地址")
		}
		site.LoginURL = loginURL
	}
	if site.AutoLoginEnabled {
		if site.CredentialRef == "" || site.LoginURL == "" ||
			site.UsernameSelector == "" || site.PasswordSelector == "" || site.SubmitSelector == "" {
			return site, errInvalid("开启自动登录需要 credential_ref、login_url 和三个确定性选择器")
		}
	}
	for _, selector := range []string{site.UsernameSelector, site.PasswordSelector, site.SubmitSelector} {
		if len([]rune(selector)) > 300 {
			return site, errInvalid("登录选择器过长")
		}
	}
	if site.IntervalSeconds == 0 {
		site.IntervalSeconds = 1800
	}
	if site.IntervalSeconds < 60 {
		return site, errInvalid("interval_seconds 不能低于 60")
	}
	if site.IntervalSeconds > 86400 {
		return site, errInvalid("interval_seconds 不能超过 86400")
	}
	if site.MaxPages <= 0 {
		site.MaxPages = 5
	}
	if site.MaxPages > 20 {
		return site, errInvalid("max_pages 不能超过 20")
	}
	if site.MaxDepth < 0 {
		site.MaxDepth = 0
	}
	if site.MaxDepth > 3 {
		return site, errInvalid("max_depth 不能超过 3")
	}
	if len(site.CredentialRef) > 200 {
		return site, errInvalid("credential_ref 过长")
	}
	site.StartURLs = startURLs
	site.AllowedOrigins = origins
	site.AllowedPathPrefixes = prefixes
	return site, nil
}

func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errInvalid("allowed_origins 必须是 http(s) origin")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

func appendUnique(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}

type webValidationError string

func (e webValidationError) Error() string { return string(e) }

func errInvalid(message string) error { return webValidationError(message) }
