package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 渠道标识。微信桌面与手机分别采集，不能共用一个开关。
const (
	ChannelActivity      = "activity"
	ChannelEmail         = "email"
	ChannelFeishu        = "feishu"
	ChannelDingTalk      = "dingtalk"
	ChannelWeChatDesktop = "wechat_desktop"
	ChannelWeChatMobile  = "wechat_mobile"
	ChannelWeb           = "web"
)

// GatedChannels 是必须显式开启后才允许采集的渠道。
var GatedChannels = []string{
	ChannelActivity, ChannelEmail, ChannelFeishu,
	ChannelDingTalk, ChannelWeChatDesktop, ChannelWeChatMobile,
}

// IsGatedChannel 报告渠道是否受 enabled 开关约束。日历等内部渠道不在本期范围内。
func IsGatedChannel(channel string) bool {
	for _, item := range GatedChannels {
		if item == channel {
			return true
		}
	}
	return false
}

// NormalizeChannel 兼容早期只区分 wechat 的调用方；桌面入口默认归为 wechat_desktop。
func NormalizeChannel(channel string) string {
	if channel == "wechat" {
		return ChannelWeChatDesktop
	}
	return channel
}

// ChannelConfig 是单渠道的独立开关，默认全部关闭。
type ChannelConfig struct {
	OwnerUserID       string    `json:"owner_user_id"`
	Channel           string    `json:"channel"`
	Enabled           bool      `json:"enabled"`
	SummaryEnabled    bool      `json:"summary_enabled"`
	IndexEnabled      bool      `json:"index_enabled"`
	BriefEnabled      bool      `json:"brief_enabled"`
	AutoWorkerEnabled bool      `json:"auto_worker_enabled"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// DefaultChannelConfig 返回未配置渠道的默认值：全部关闭。
func DefaultChannelConfig(ownerID, channel string) ChannelConfig {
	return ChannelConfig{OwnerUserID: ownerID, Channel: channel}
}

type ChannelConfigRepo struct{ q Querier }

func NewChannelConfigRepo(q Querier) *ChannelConfigRepo { return &ChannelConfigRepo{q: q} }
func (d *DB) ChannelConfigs() *ChannelConfigRepo        { return NewChannelConfigRepo(d) }

// Get 返回渠道开关；没有记录时返回全关闭的默认值，不返回 ErrNotFound。
func (r *ChannelConfigRepo) Get(ctx context.Context, ownerID, channel string) (ChannelConfig, error) {
	item := DefaultChannelConfig(ownerID, NormalizeChannel(channel))
	var enabled, summary, index, brief, autoWorker int
	err := r.q.QueryRowContext(ctx, `SELECT enabled, summary_enabled, index_enabled,
		brief_enabled, auto_worker_enabled, updated_at FROM agent_channel_config
		WHERE owner_user_id = ? AND channel = ?`, ownerID, item.Channel).
		Scan(&enabled, &summary, &index, &brief, &autoWorker, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, nil
	}
	if err != nil {
		return item, err
	}
	item.Enabled = enabled == 1
	item.SummaryEnabled = summary == 1
	item.IndexEnabled = index == 1
	item.BriefEnabled = brief == 1
	item.AutoWorkerEnabled = autoWorker == 1
	return item, nil
}

// List 始终返回全部受控渠道，未写入记录的渠道使用默认关闭值。
func (r *ChannelConfigRepo) List(ctx context.Context, ownerID string) ([]ChannelConfig, error) {
	out := make([]ChannelConfig, 0, len(GatedChannels))
	for _, channel := range GatedChannels {
		item, err := r.Get(ctx, ownerID, channel)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// Save 覆盖一个渠道的全部开关。
func (r *ChannelConfigRepo) Save(ctx context.Context, item ChannelConfig, at time.Time) error {
	item.Channel = NormalizeChannel(item.Channel)
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_channel_config
		(owner_user_id, channel, enabled, summary_enabled, index_enabled,
		 brief_enabled, auto_worker_enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id, channel) DO UPDATE SET
		enabled = excluded.enabled,
		summary_enabled = excluded.summary_enabled,
		index_enabled = excluded.index_enabled,
		brief_enabled = excluded.brief_enabled,
		auto_worker_enabled = excluded.auto_worker_enabled,
		updated_at = excluded.updated_at`,
		item.OwnerUserID, item.Channel, boolInt(item.Enabled), boolInt(item.SummaryEnabled),
		boolInt(item.IndexEnabled), boolInt(item.BriefEnabled), boolInt(item.AutoWorkerEnabled), at)
	return err
}

func (r *ChannelConfigRepo) Enabled(ctx context.Context, ownerID, channel string) (bool, error) {
	item, err := r.Get(ctx, ownerID, channel)
	return item.Enabled, err
}

func (r *ChannelConfigRepo) SummaryEnabled(ctx context.Context, ownerID, channel string) (bool, error) {
	item, err := r.Get(ctx, ownerID, channel)
	return item.SummaryEnabled, err
}

func (r *ChannelConfigRepo) IndexEnabled(ctx context.Context, ownerID, channel string) (bool, error) {
	item, err := r.Get(ctx, ownerID, channel)
	return item.IndexEnabled, err
}

func (r *ChannelConfigRepo) BriefEnabled(ctx context.Context, ownerID, channel string) (bool, error) {
	item, err := r.Get(ctx, ownerID, channel)
	return item.BriefEnabled, err
}

func (r *ChannelConfigRepo) AutoWorkerEnabled(ctx context.Context, ownerID, channel string) (bool, error) {
	item, err := r.Get(ctx, ownerID, channel)
	return item.AutoWorkerEnabled, err
}

// GlobalConfig 是不与渠道采集绑定的全局能力开关。
type GlobalConfig struct {
	OwnerUserID            string    `json:"owner_user_id"`
	RouterEnabled          bool      `json:"router_enabled"`
	WorkerEnabled          bool      `json:"worker_enabled"`
	DailyBriefEnabled      bool      `json:"daily_brief_enabled"`
	ProactiveNotifyEnabled bool      `json:"proactive_notify_enabled"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func DefaultGlobalConfig(ownerID string) GlobalConfig {
	return GlobalConfig{
		OwnerUserID: ownerID, RouterEnabled: true, WorkerEnabled: true,
		DailyBriefEnabled: true, ProactiveNotifyEnabled: true,
	}
}

type GlobalConfigRepo struct{ q Querier }

func NewGlobalConfigRepo(q Querier) *GlobalConfigRepo { return &GlobalConfigRepo{q: q} }
func (d *DB) GlobalConfigs() *GlobalConfigRepo        { return NewGlobalConfigRepo(d) }

func (r *GlobalConfigRepo) Get(ctx context.Context, ownerID string) (GlobalConfig, error) {
	item := DefaultGlobalConfig(ownerID)
	var router, worker, brief, notify int
	err := r.q.QueryRowContext(ctx, `SELECT router_enabled, worker_enabled,
		daily_brief_enabled, proactive_notify_enabled, updated_at
		FROM agent_global_config WHERE owner_user_id = ?`, ownerID).
		Scan(&router, &worker, &brief, &notify, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, nil
	}
	if err != nil {
		return item, err
	}
	item.RouterEnabled = router == 1
	item.WorkerEnabled = worker == 1
	item.DailyBriefEnabled = brief == 1
	item.ProactiveNotifyEnabled = notify == 1
	return item, nil
}

func (r *GlobalConfigRepo) Save(ctx context.Context, item GlobalConfig, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_global_config
		(owner_user_id, router_enabled, worker_enabled, daily_brief_enabled,
		 proactive_notify_enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id) DO UPDATE SET
		router_enabled = excluded.router_enabled,
		worker_enabled = excluded.worker_enabled,
		daily_brief_enabled = excluded.daily_brief_enabled,
		proactive_notify_enabled = excluded.proactive_notify_enabled,
		updated_at = excluded.updated_at`,
		item.OwnerUserID, boolInt(item.RouterEnabled), boolInt(item.WorkerEnabled),
		boolInt(item.DailyBriefEnabled), boolInt(item.ProactiveNotifyEnabled), at)
	return err
}
