package agenthub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Provider struct {
	ID              string  `json:"id"`
	AppType         string  `json:"app_type"`
	Name            string  `json:"name"`
	Current         bool    `json:"current"`
	Healthy         bool    `json:"healthy"`
	LimitDailyUSD   string  `json:"limit_daily_usd,omitempty"`
	LimitMonthlyUSD string  `json:"limit_monthly_usd,omitempty"`
	UsedTodayUSD    float64 `json:"used_today_usd"`
	UsedMonthUSD    float64 `json:"used_month_usd"`
	CanSwitch       bool    `json:"can_switch"`
}

type CCSwitch struct {
	DBPath  string
	HomeDir string
}

func NewCCSwitch(dbPath, homeDir string) *CCSwitch {
	return &CCSwitch{DBPath: dbPath, HomeDir: homeDir}
}

func (c *CCSwitch) Providers(ctx context.Context, appType string) ([]Provider, error) {
	if !fileExists(c.DBPath) {
		return nil, errors.New("未发现 CC Switch 数据库")
	}
	db, err := sql.Open("sqlite", c.DBPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	query := `SELECT p.id,p.app_type,p.name,p.is_current,COALESCE(p.limit_daily_usd,''),COALESCE(p.limit_monthly_usd,''),COALESCE(h.is_healthy,1)
		FROM providers p LEFT JOIN provider_health h ON h.provider_id=p.id AND h.app_type=p.app_type`
	args := []any{}
	if appType != "" {
		query += ` WHERE p.app_type=?`
		args = append(args, appType)
	}
	query += ` ORDER BY p.app_type,p.is_current DESC,p.sort_index,p.name`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	providers := []Provider{}
	for rows.Next() {
		var p Provider
		var current, healthy int
		if err := rows.Scan(&p.ID, &p.AppType, &p.Name, &current, &p.LimitDailyUSD, &p.LimitMonthlyUSD, &healthy); err != nil {
			return nil, err
		}
		p.Current, p.Healthy = current != 0, healthy != 0
		p.CanSwitch = p.AppType == "claude"
		providers = append(providers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range providers {
		providers[i].UsedTodayUSD = usageSum(ctx, db, providers[i], now.Format("2006-01-02"), now.Format("2006-01-02"))
		providers[i].UsedMonthUSD = usageSum(ctx, db, providers[i], now.Format("2006-01")+"-01", now.Format("2006-01-02"))
	}
	return providers, nil
}

func usageSum(ctx context.Context, db *sql.DB, provider Provider, first, last string) float64 {
	var value float64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CAST(total_cost_usd AS REAL)),0) FROM usage_daily_rollups
		WHERE provider_id=? AND app_type=? AND date>=? AND date<=?`, provider.ID, provider.AppType, first, last).Scan(&value)
	if err != nil {
		return 0
	}
	return value
}

func (c *CCSwitch) SwitchClaude(ctx context.Context, providerID string) (Provider, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return Provider{}, errors.New("供应商 ID 不能为空")
	}
	if !fileExists(c.DBPath) {
		return Provider{}, errors.New("未发现 CC Switch 数据库")
	}
	db, err := sql.Open("sqlite", c.DBPath)
	if err != nil {
		return Provider{}, err
	}
	defer db.Close()
	var routing int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(proxy_enabled),0) FROM proxy_config WHERE app_type='claude'`).Scan(&routing)
	if routing != 0 {
		return Provider{}, errors.New("Claude 正在使用 CC Switch 路由模式，请在 CC Switch 中切换路由目标")
	}
	var name, config string
	err = db.QueryRowContext(ctx, `SELECT name,settings_config FROM providers WHERE id=? AND app_type='claude'`, providerID).Scan(&name, &config)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, errors.New("未找到 Claude Code 供应商")
	}
	if err != nil {
		return Provider{}, err
	}
	var providerSettings map[string]any
	if err := json.Unmarshal([]byte(config), &providerSettings); err != nil {
		return Provider{}, errors.New("供应商配置不是有效 JSON，未做任何修改")
	}
	providerEnv, _ := providerSettings["env"].(map[string]any)
	path := filepath.Join(c.HomeDir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Provider{}, err
	}
	oldRaw, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Provider{}, readErr
	}
	live := map[string]any{}
	if len(oldRaw) > 0 {
		if err := json.Unmarshal(oldRaw, &live); err != nil {
			return Provider{}, errors.New("Claude settings.json 无法解析，未做任何修改")
		}
	}
	liveEnv, _ := live["env"].(map[string]any)
	if liveEnv == nil {
		liveEnv = map[string]any{}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		delete(liveEnv, key)
	}
	for key, value := range providerEnv {
		liveEnv[key] = value
	}
	live["env"] = liveEnv
	newRaw, _ := json.MarshalIndent(live, "", "  ")
	if len(oldRaw) > 0 {
		backup := path + ".goldenfinger-" + time.Now().Format("20060102-150405") + ".bak"
		if err := os.WriteFile(backup, oldRaw, 0o600); err != nil {
			return Provider{}, fmt.Errorf("备份 Claude 配置失败：%w", err)
		}
	}
	tmp := path + ".goldenfinger.tmp"
	if err := os.WriteFile(tmp, append(newRaw, '\n'), 0o600); err != nil {
		return Provider{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Provider{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		restoreFile(path, oldRaw)
		return Provider{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET is_current=CASE WHEN id=? THEN 1 ELSE 0 END WHERE app_type='claude'`, providerID); err != nil {
		_ = tx.Rollback()
		restoreFile(path, oldRaw)
		return Provider{}, err
	}
	if err := tx.Commit(); err != nil {
		restoreFile(path, oldRaw)
		return Provider{}, err
	}
	return Provider{ID: providerID, AppType: "claude", Name: name, Current: true, Healthy: true, CanSwitch: true}, nil
}

func restoreFile(path string, raw []byte) {
	if len(raw) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}
