// Package collection 管理机主明确授权的采集渠道。
// 模型只能通过这个包修改配置，不能接触微信数据库、密钥或连接进程。
package collection

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
)

const (
	ChannelWeChat   = store.ChannelWeChatDesktop
	ChannelWeb      = store.ChannelWeb
	ChannelActivity = store.ChannelActivity
)

// WeChatChat 是桌面发现返回的安全候选字段，不包含消息正文或摘要。
type WeChatChat struct {
	SourceID      string `json:"source_id"`
	ChatKey       string `json:"chat_key"`
	DisplayName   string `json:"display_name"`
	ChatType      string `json:"chat_type"`
	AccountID     string `json:"account_id"`
	LastMessageAt string `json:"last_message_at"`
}

// ConnectorRequest 是服务端等待桌面执行的短命命令。
type ConnectorRequest struct {
	ID          string `json:"id"`
	OwnerUserID string `json:"-"`
	Channel     string `json:"channel"`
	Kind        string `json:"kind"`
	Query       string `json:"query,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
}

// ConnectorResult 只承载配置所需结果，不允许回传任意桌面数据。
type ConnectorResult struct {
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Chats   []WeChatChat `json:"chats,omitempty"`
	Enabled *bool        `json:"enabled,omitempty"`
}

type pendingRequest struct {
	request ConnectorRequest
	done    chan ConnectorResult
}

// Manager 协调服务端配置和桌面真实采集开关。
type Manager struct {
	Repos          *store.Repos
	Settings       *settings.Store
	Now            func() time.Time
	RequestTimeout time.Duration

	mu      sync.Mutex
	pending map[string]*pendingRequest
}

func New(repos *store.Repos, settingsStore *settings.Store) *Manager {
	return &Manager{Repos: repos, Settings: settingsStore, Now: time.Now,
		RequestTimeout: 12 * time.Second, pending: map[string]*pendingRequest{}}
}

// ConfigureInput 是聊天工具传入的渠道修改请求。
type ConfigureInput struct {
	Channel         string
	Action          string
	Query           string
	SourceID        string
	URL             string
	SiteID          string
	Scope           string
	IntervalSeconds int
}

// Outcome 中 Question 非空表示必须让机主在同名候选中明确选择。
type Outcome struct {
	Data     map[string]any
	Question string
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// List 返回真实支持的渠道与安全配置，不返回凭据、网页正文或微信摘要。
func (m *Manager) List(ctx context.Context, ownerID string) (map[string]any, error) {
	if m == nil || m.Repos == nil || m.Settings == nil {
		return nil, errors.New("采集渠道管理未装配")
	}
	wechat, err := m.Repos.ChannelConfigs.Get(ctx, ownerID, ChannelWeChat)
	if err != nil {
		return nil, err
	}
	activity, err := m.Repos.ChannelConfigs.Get(ctx, ownerID, ChannelActivity)
	if err != nil {
		return nil, err
	}
	policies, err := m.Repos.ChannelPolicies.List(ctx, ownerID, ChannelWeChat)
	if err != nil {
		return nil, err
	}
	sources := make([]map[string]any, 0)
	for _, policy := range policies {
		if policy.Source == "" || !strings.HasPrefix(policy.Source, "wechat-db:") {
			continue
		}
		sources = append(sources, map[string]any{"source_id": policy.Source, "allowed": policy.Allowed})
	}
	sites := make([]map[string]any, 0)
	for _, site := range m.Settings.Web().Sites {
		sites = append(sites, safeSite(site))
	}
	return map[string]any{
		"capabilities":   []string{"wechat_desktop", "web", "activity"},
		"wechat_desktop": map[string]any{"enabled": wechat.Enabled, "sources": sources, "sync_scope": "latest_only"},
		"web":            map[string]any{"sites": sites, "default_scope": "current_page"},
		"activity": map[string]any{"configured_enabled": activity.Enabled,
			"scope": "desktop_work_trail", "note": "这是整个桌面工作轨迹/协作模式的总开关，不是单应用白名单。"},
	}, nil
}

func safeSite(site settings.WebSite) map[string]any {
	scope := "current_page"
	if site.NavigationEnabled || site.MaxDepth > 0 || site.MaxPages > 1 {
		scope = "site"
	}
	return map[string]any{
		"id": site.ID, "start_urls": append([]string(nil), site.StartURLs...), "enabled": site.Enabled,
		"scope": scope, "interval_seconds": site.IntervalSeconds,
		"max_pages": site.MaxPages, "max_depth": site.MaxDepth,
	}
}

// Configure 应用机主明确要求的配置修改。
func (m *Manager) Configure(ctx context.Context, ownerID string, input ConfigureInput) (Outcome, error) {
	if m == nil || m.Repos == nil || m.Settings == nil {
		return Outcome{}, errors.New("采集渠道管理未装配")
	}
	input.Channel = strings.TrimSpace(input.Channel)
	input.Action = strings.TrimSpace(input.Action)
	switch input.Channel {
	case ChannelWeChat:
		return m.configureWeChat(ctx, ownerID, input)
	case ChannelWeb:
		return m.configureWeb(ctx, ownerID, input)
	case ChannelActivity:
		return m.configureActivity(ctx, ownerID, input)
	default:
		return Outcome{}, fmt.Errorf("首版只支持 wechat_desktop、web、activity，未接入 %q", input.Channel)
	}
}

func (m *Manager) setChannelEnabled(ctx context.Context, ownerID, channel string, enabled bool) (store.ChannelConfig, error) {
	item, err := m.Repos.ChannelConfigs.Get(ctx, ownerID, channel)
	if err != nil {
		return item, err
	}
	item.Enabled = enabled
	if enabled {
		// 新增采集默认允许总结和检索；自动执行仍保持原设置，绝不隐式放权。
		item.SummaryEnabled = true
		item.IndexEnabled = true
		item.BriefEnabled = true
	}
	return item, m.Repos.ChannelConfigs.Save(ctx, item, m.now())
}

func (m *Manager) configureWeChat(ctx context.Context, ownerID string, input ConfigureInput) (Outcome, error) {
	switch input.Action {
	case "enable", "disable":
		item, err := m.setChannelEnabled(ctx, ownerID, ChannelWeChat, input.Action == "enable")
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Data: map[string]any{"channel": ChannelWeChat, "enabled": item.Enabled}}, nil
	case "add":
		if _, err := m.setChannelEnabled(ctx, ownerID, ChannelWeChat, true); err != nil {
			return Outcome{}, err
		}
		chat, outcome, err := m.resolveWeChatChat(ctx, ownerID, input)
		if err != nil || outcome != nil {
			if outcome != nil {
				return *outcome, nil
			}
			return Outcome{}, err
		}
		if err := m.Repos.ChannelPolicies.Set(ctx, ownerID, ChannelWeChat, chat.SourceID, true, m.now()); err != nil {
			return Outcome{}, err
		}
		return Outcome{Data: map[string]any{"channel": ChannelWeChat, "action": "added", "enabled": true,
			"chat": chat, "sync_scope": "latest_only"}}, nil
	case "remove":
		sourceID := strings.TrimSpace(input.SourceID)
		if !strings.HasPrefix(sourceID, "wechat-db:") {
			return Outcome{}, errors.New("移除微信采集需要 list_collection_channels 返回的稳定 source_id")
		}
		if err := m.Repos.ChannelPolicies.Set(ctx, ownerID, ChannelWeChat, sourceID, false, m.now()); err != nil {
			return Outcome{}, err
		}
		return Outcome{Data: map[string]any{"channel": ChannelWeChat, "action": "removed", "source_id": sourceID, "allowed": false}}, nil
	default:
		return Outcome{}, fmt.Errorf("微信采集不支持操作 %q", input.Action)
	}
}

func (m *Manager) resolveWeChatChat(ctx context.Context, ownerID string, input ConfigureInput) (WeChatChat, *Outcome, error) {
	if sourceID := strings.TrimSpace(input.SourceID); sourceID != "" {
		if !strings.HasPrefix(sourceID, "wechat-db:") {
			return WeChatChat{}, nil, errors.New("source_id 必须是桌面发现的稳定微信来源")
		}
		return WeChatChat{SourceID: sourceID}, nil, nil
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return WeChatChat{}, nil, errors.New("增加微信联系人或群需要 query（名称），或使用候选的 source_id")
	}
	result, err := m.Request(ctx, ConnectorRequest{OwnerUserID: ownerID, Channel: ChannelWeChat,
		Kind: "list_wechat_chats", Query: query, Limit: 20})
	if err != nil {
		return WeChatChat{}, nil, err
	}
	if !result.OK {
		return WeChatChat{}, nil, errors.New(firstText(result.Error, "桌面微信发现失败"))
	}
	chats := make([]WeChatChat, 0, len(result.Chats))
	for _, chat := range result.Chats {
		chat.SourceID = strings.TrimSpace(chat.SourceID)
		chat.ChatKey = strings.TrimSpace(chat.ChatKey)
		chat.DisplayName = strings.TrimSpace(chat.DisplayName)
		if strings.HasPrefix(chat.SourceID, "wechat-db:") && chat.ChatKey != "" {
			chats = append(chats, chat)
		}
	}
	if len(chats) == 0 {
		return WeChatChat{}, nil, fmt.Errorf("桌面微信里没有找到名称包含 %q 的会话", query)
	}
	if len(chats) > 1 {
		return WeChatChat{}, &Outcome{Data: map[string]any{"candidates": chats},
			Question: fmt.Sprintf("找到 %d 个同名或相近会话。请告诉我要采集哪一个（可用候选序号或 source_id），我不会替你猜。", len(chats))}, nil
	}
	return chats[0], nil, nil
}

func (m *Manager) configureWeb(_ context.Context, _ string, input ConfigureInput) (Outcome, error) {
	switch input.Action {
	case "add":
		site, err := webSiteFromInput(input)
		if err != nil {
			return Outcome{}, err
		}
		if err := m.Settings.UpsertSite(site); err != nil {
			return Outcome{}, err
		}
		return Outcome{Data: map[string]any{"channel": ChannelWeb, "action": "added", "site": safeSite(site)}}, nil
	case "enable", "disable", "remove":
		site, found, err := m.findSite(input)
		if err != nil {
			return Outcome{}, err
		}
		if !found {
			return Outcome{}, errors.New("没有找到这个网页采集配置，请先用 list_collection_channels 查看 site_id")
		}
		if input.Action == "remove" {
			if err := m.Settings.DeleteSite(site.ID); err != nil {
				return Outcome{}, err
			}
			return Outcome{Data: map[string]any{"channel": ChannelWeb, "action": "removed", "site_id": site.ID}}, nil
		}
		site.Enabled = input.Action == "enable"
		if err := m.Settings.UpsertSite(site); err != nil {
			return Outcome{}, err
		}
		return Outcome{Data: map[string]any{"channel": ChannelWeb, "action": "updated", "site": safeSite(site)}}, nil
	default:
		return Outcome{}, fmt.Errorf("网页采集不支持操作 %q", input.Action)
	}
}

func webSiteFromInput(input ConfigureInput) (settings.WebSite, error) {
	raw := strings.TrimSpace(input.URL)
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return settings.WebSite{}, errors.New("网页采集需要有效的 http(s) URL")
	}
	if parsed.User != nil {
		return settings.WebSite{}, errors.New("URL 不能包含账号或密码，请在设置页安全配置登录凭据")
	}
	parsed.Fragment = ""
	canonical := parsed.String()
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = "current_page"
	}
	if scope != "current_page" && scope != "site" {
		return settings.WebSite{}, errors.New("网页 scope 只支持 current_page 或明确授权的 site")
	}
	interval := input.IntervalSeconds
	if interval == 0 {
		interval = 1800
	}
	if interval < 60 || interval > 86400 {
		return settings.WebSite{}, errors.New("网页采集间隔必须在 60 到 86400 秒之间")
	}
	hash := sha256.Sum256([]byte(canonical))
	id := "web-" + hex.EncodeToString(hash[:8])
	site := settings.WebSite{ID: id, Enabled: true, StartURLs: []string{canonical},
		AllowedOrigins:      []string{strings.ToLower(parsed.Scheme + "://" + parsed.Host)},
		AllowedPathPrefixes: []string{parsed.Path}, IntervalSeconds: interval,
		MaxPages: 1, MaxDepth: 0, SummaryEnabled: true, IndexEnabled: true, BriefEnabled: true,
		NavigationEnabled: false, SessionEnabled: false, AutoLoginEnabled: false}
	if scope == "site" {
		site.MaxPages = 20
		site.MaxDepth = 2
		site.NavigationEnabled = true
	}
	return site, nil
}

func (m *Manager) findSite(input ConfigureInput) (settings.WebSite, bool, error) {
	if id := strings.TrimSpace(input.SiteID); id != "" {
		site, ok := m.Settings.Site(id)
		return site, ok, nil
	}
	if raw := strings.TrimSpace(input.URL); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return settings.WebSite{}, false, errors.New("URL 格式无效")
		}
		parsed.Fragment = ""
		hash := sha256.Sum256([]byte(parsed.String()))
		site, ok := m.Settings.Site("web-" + hex.EncodeToString(hash[:8]))
		return site, ok, nil
	}
	return settings.WebSite{}, false, errors.New("启停或移除网页采集需要 site_id 或原 URL")
}

func (m *Manager) configureActivity(ctx context.Context, ownerID string, input ConfigureInput) (Outcome, error) {
	var enabled bool
	switch input.Action {
	case "add", "enable":
		enabled = true
	case "remove", "disable":
		enabled = false
	default:
		return Outcome{}, fmt.Errorf("桌面工作轨迹不支持操作 %q", input.Action)
	}
	result, err := m.Request(ctx, ConnectorRequest{OwnerUserID: ownerID, Channel: ChannelActivity,
		Kind: "set_activity", Enabled: enabled})
	if err != nil {
		return Outcome{}, err
	}
	if !result.OK {
		return Outcome{}, errors.New(firstText(result.Error, "桌面工作轨迹切换失败"))
	}
	if result.Enabled == nil || *result.Enabled != enabled {
		return Outcome{}, errors.New("桌面端没有确认工作轨迹开关状态，本次不保存配置")
	}
	item, err := m.setChannelEnabled(ctx, ownerID, ChannelActivity, enabled)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Data: map[string]any{"channel": ChannelActivity, "enabled": item.Enabled,
		"scope": "desktop_work_trail", "note": "这是整个桌面工作轨迹/协作模式的总开关，不是单应用白名单。"}}, nil
}

// Request 注册一个桌面命令并等待真实结果；请求发出不等于配置成功。
func (m *Manager) Request(ctx context.Context, request ConnectorRequest) (ConnectorResult, error) {
	if request.OwnerUserID == "" || request.Channel == "" || request.Kind == "" {
		return ConnectorResult{}, errors.New("桌面请求缺少 owner、channel 或 kind")
	}
	id, err := randomID()
	if err != nil {
		return ConnectorResult{}, err
	}
	request.ID = id
	item := &pendingRequest{request: request, done: make(chan ConnectorResult, 1)}
	m.mu.Lock()
	if m.pending == nil {
		m.pending = map[string]*pendingRequest{}
	}
	m.pending[id] = item
	m.mu.Unlock()
	timeout := m.RequestTimeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-item.done:
		return result, nil
	case <-timer.C:
		m.mu.Lock()
		if m.pending[id] == item {
			delete(m.pending, id)
		}
		m.mu.Unlock()
		return ConnectorResult{}, errors.New("桌面端未响应，请先启动桌面版后重试")
	case <-ctx.Done():
		m.mu.Lock()
		if m.pending[id] == item {
			delete(m.pending, id)
		}
		m.mu.Unlock()
		return ConnectorResult{}, ctx.Err()
	}
}

// Pending 只返回当前机主和渠道的待执行命令。
func (m *Manager) Pending(ownerID, channel string) []ConnectorRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ConnectorRequest, 0)
	for _, item := range m.pending {
		if item.request.OwnerUserID == ownerID && (channel == "" || item.request.Channel == channel) {
			out = append(out, item.request)
		}
	}
	return out
}

// Complete 完成一个待执行命令。不同机主不能完成彼此的请求。
func (m *Manager) Complete(ownerID, id string, result ConnectorResult) bool {
	m.mu.Lock()
	item, ok := m.pending[id]
	if !ok || item.request.OwnerUserID != ownerID {
		m.mu.Unlock()
		return false
	}
	delete(m.pending, id)
	m.mu.Unlock()
	result.Error = strings.TrimSpace(result.Error)
	if len([]rune(result.Error)) > 300 {
		result.Error = string([]rune(result.Error)[:300])
	}
	item.done <- result
	return true
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "connector-" + hex.EncodeToString(raw[:]), nil
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
