package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/store"
)

// 渠道在手机上的三种真实状态。
//
// 不能把「开关保存成功」当成「连接器已经采集」：桌面连接器有独立的心跳与状态上报。
const (
	// ChannelAvailabilityLocal 表示该能力在本机就能用（如手机通知微信）。
	ChannelAvailabilityLocal = "local"
	// ChannelAvailabilityDesktop 表示该能力必须在电脑在线时才有真实效果。
	ChannelAvailabilityDesktop = "desktop"
	// ChannelAvailabilityUnsupported 表示还没有真实连接器，不应假装可用。
	ChannelAvailabilityUnsupported = "unsupported"
)

// channelDescriptor 描述一个渠道在哪台设备上生效、当前是否真的在跑。
type channelDescriptor struct {
	Channel string `json:"channel"`
	Label   string `json:"label"`
	// Availability 取上文的 local/desktop/unsupported 三态。
	Availability string `json:"availability"`
	// Device 说明生效设备：local/desktop/none。
	Device string `json:"device"`
	// Enabled 是当前开关状态。
	Enabled bool `json:"enabled"`
	// ConnectorState 是连接器最近一次上报的状态；没有上报时为空。
	ConnectorState string `json:"connector_state,omitempty"`
	// LastHeartbeat 是连接器最后上报时间。
	LastHeartbeat string `json:"last_heartbeat,omitempty"`
	// Note 解释这台设备上到底会发生什么。
	Note string `json:"note"`
}

// channelDescriptors 列出全部受控渠道及其真实生效设备。
//
// 三态划分（勿删）：
//   - wechat_mobile 是手机本机通知监听，本机可用；
//   - wechat_desktop / activity / web 需要电脑上的真实连接器，电脑在线才生效；
//   - feishu / dingtalk / email 尚无真实连接器，标为未接入并禁用。
func channelDescriptors() []channelDescriptor {
	return []channelDescriptor{
		{
			Channel: store.ChannelWeChatMobile, Label: "微信（手机通知）",
			Availability: ChannelAvailabilityLocal, Device: "local",
			Note: "由手机通知监听采集，在本机生效，不需要电脑在线。",
		},
		{
			Channel: store.ChannelWeChatDesktop, Label: "微信（电脑桌面）",
			Availability: ChannelAvailabilityDesktop, Device: "desktop",
			Note: "读取电脑本机微信数据库，需要电脑上的采集器真实运行。",
		},
		{
			Channel: store.ChannelActivity, Label: "工作轨迹",
			Availability: ChannelAvailabilityDesktop, Device: "desktop",
			Note: "采集电脑上的工作轨迹与协作模式记录，需要电脑在线。",
		},
		{
			Channel: store.ChannelWeb, Label: "授权网页",
			Availability: ChannelAvailabilityDesktop, Device: "desktop",
			Note: "按授权范围抓取网页正文，由电脑端的采集器执行。",
		},
		{
			Channel: store.ChannelFeishu, Label: "飞书",
			Availability: ChannelAvailabilityUnsupported, Device: "none",
			Note: "尚无真实连接器，未接入。",
		},
		{
			Channel: store.ChannelDingTalk, Label: "钉钉",
			Availability: ChannelAvailabilityUnsupported, Device: "none",
			Note: "尚无真实连接器，未接入。",
		},
		{
			Channel: store.ChannelEmail, Label: "邮箱",
			Availability: ChannelAvailabilityUnsupported, Device: "none",
			Note: "尚无真实连接器，未接入。",
		},
	}
}

// GET /api/collection/channels
//
// 返回桌面侧采集渠道的真实状态：开关、连接器心跳与生效设备。
// 手机用它显示「在哪台设备生效、当前是否真的运行」。
func (s *Server) handleListCollectionChannels(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	configs, err := s.Repos.ChannelConfigs.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enabled := map[string]bool{}
	for _, item := range configs {
		enabled[item.Channel] = item.Enabled
	}
	items := make([]channelDescriptor, 0, len(channelDescriptors()))
	for _, item := range channelDescriptors() {
		item.Enabled = enabled[item.Channel]
		if status, err := s.Repos.ConnectorStatuses.Get(r.Context(), u.ID, item.Channel); err == nil {
			item.ConnectorState = status.State
			item.LastHeartbeat = status.UpdatedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	body := map[string]any{"channels": items}
	// 电脑端采集管理的详细视图（source_id / site_id 等）也一并返回，
	// 手机端的聊天工具要靠它给出可执行的候选。
	if s.Collection != nil {
		if detail, err := s.Collection.List(r.Context(), u.ID); err == nil {
			body["detail"] = detail
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// POST /api/collection/channels
//
// 配置桌面采集渠道。必须等桌面的真实回执：
// 失败时保留原状态并如实报错，不能让手机显示成已生效。
func (s *Server) handleConfigureCollectionChannel(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	if s.Collection == nil {
		writeError(w, http.StatusServiceUnavailable, "采集渠道管理未启用")
		return
	}
	var req struct {
		Channel         string `json:"channel"`
		Action          string `json:"action"`
		Enabled         *bool  `json:"enabled"`
		Query           string `json:"query"`
		SourceID        string `json:"source_id"`
		URL             string `json:"url"`
		SiteID          string `json:"site_id"`
		Scope           string `json:"scope"`
		IntervalSeconds int    `json:"interval_seconds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	channel := store.NormalizeChannel(strings.TrimSpace(req.Channel))
	action := strings.TrimSpace(req.Action)
	if action == "" {
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "需要 action 或 enabled")
			return
		}
		action = "disable"
		if *req.Enabled {
			action = "enable"
		}
	}
	outcome, err := s.Collection.Configure(r.Context(), u.ID, collection.ConfigureInput{
		Channel: channel, Action: action, Query: req.Query, SourceID: req.SourceID,
		URL: req.URL, SiteID: req.SiteID, Scope: req.Scope,
		IntervalSeconds: req.IntervalSeconds,
	})
	if err != nil {
		// 桌面拒绝时保留原状态，把真实原因带回手机。
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": true, "channel": channel, "action": action,
		"data": outcome.Data, "question": outcome.Question,
	})
}
