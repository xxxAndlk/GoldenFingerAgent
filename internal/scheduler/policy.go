// Package scheduler: 提醒策略（免打扰/升级/推送预算）
// 与基于数据库的 tick 循环（日后可替换为 Temporal Cloud）。
package scheduler

import (
	"time"
	_ "time/tzdata" // 内嵌时区数据库（Windows 没有系统 tzdata）

	"goldenfinger/agent/internal/store"
)

// Policy 持有免打扰时段、升级与预算规则（配置驱动）。
type Policy struct {
	DNDStartHour       int  // 含，默认 22
	DNDEndHour         int  // 不含，默认 7
	ChildNightSilence  bool // 儿童：任何情况都不得打破免打扰时段
	UrgentBreaksDNDFor map[string]bool
	AckTimeout         time.Duration
	MaxLevel           int
	MaxDailyPush       int
}

// DefaultPolicy 与文档默认值一致。
func DefaultPolicy() Policy {
	return Policy{
		DNDStartHour:       22,
		DNDEndHour:         7,
		ChildNightSilence:  true,
		UrgentBreaksDNDFor: map[string]bool{store.UserElder: true, store.UserGeneral: true},
		AckTimeout:         30 * time.Minute,
		MaxLevel:           3,
		MaxDailyPush:       3,
	}
}

// InDND 报告 t 是否落在用户的免打扰时段内。
func (p Policy) InDND(t time.Time, loc *time.Location) bool {
	h := t.In(loc).Hour()
	if p.DNDStartHour > p.DNDEndHour { // 跨零点，例如 22→7
		return h >= p.DNDStartHour || h < p.DNDEndHour
	}
	return h >= p.DNDStartHour && h < p.DNDEndHour
}

// DNDWindowEnd 返回免打扰时段结束、可以投递的下一个时刻。
func (p Policy) DNDWindowEnd(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	h := local.Hour()
	switch {
	case p.DNDStartHour > p.DNDEndHour:
		if h >= p.DNDStartHour {
				// 处于晚间段 → 明天早上
			return time.Date(local.Year(), local.Month(), local.Day(), p.DNDEndHour, 0, 0, 0, loc).AddDate(0, 0, 1)
		}
		if h < p.DNDEndHour {
				// 处于早晨段 → 今天
			return time.Date(local.Year(), local.Month(), local.Day(), p.DNDEndHour, 0, 0, 0, loc)
		}
	default:
		if h >= p.DNDStartHour {
			return time.Date(local.Year(), local.Month(), local.Day(), p.DNDEndHour, 0, 0, 0, loc).AddDate(0, 0, 1)
		}
	}
	return local
}

// AllowFire 决定该用户的提醒是否可以在 fireAt 触发。
// 延迟时，deferTo 携带调整后的触发时间。
func (p Policy) AllowFire(user *store.User, level int, fireAt time.Time) (allow bool, deferTo time.Time) {
	loc := loadLocation(user.TZ)
	if !p.InDND(fireAt, loc) {
		return true, fireAt
	}
	// 儿童：绝对夜间静默——任何情况都不得打破时段。
	if user.UserType == store.UserChild && p.ChildNightSilence {
		return false, p.DNDWindowEnd(fireAt, loc)
	}
	// 紧急（最高级）可为配置的用户类型打破免打扰。
	if level >= p.MaxLevel && p.UrgentBreaksDNDFor[user.UserType] {
		return true, fireAt
	}
	// 其余一律延迟到时段结束。
	return false, p.DNDWindowEnd(fireAt, loc)
}

// NextLevel 返回未确认投递后的升级级别。
func (p Policy) NextLevel(level int) (int, bool) {
	if level >= p.MaxLevel {
		return level, false
	}
	return level + 1, true
}

// ChannelForLevel 把级别映射到其投递渠道。
func (p Policy) ChannelForLevel(level int) string {
	switch level {
	case 1:
		return "app"
	case 2:
		return "push"
	default:
		return "sms"
	}
}

// OverPushBudget 报告用户是否已达到今日非紧急推送上限。
func (p Policy) OverPushBudget(sentToday int) bool {
	return sentToday >= p.MaxDailyPush
}

func loadLocation(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.FixedZone(tz, 8*3600) // 对 Asia/Shanghai 风格的名称回退到 CST
	}
	return loc
}
