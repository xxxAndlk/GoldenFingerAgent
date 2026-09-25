// Package scheduler: the reminder policy (DND / escalation / push budget)
// and the DB-backed tick loop (swappable for Temporal Cloud later).
package scheduler

import (
	"time"
	_ "time/tzdata" // embed tz database (Windows has no system tzdata)

	"goldenfinger/agent/internal/store"
)

// Policy holds DND windows, escalation and budget rules (config-driven).
type Policy struct {
	DNDStartHour       int  // inclusive, default 22
	DNDEndHour         int  // exclusive, default 7
	ChildNightSilence  bool // children: nothing breaks the window, ever
	UrgentBreaksDNDFor map[string]bool
	AckTimeout         time.Duration
	MaxLevel           int
	MaxDailyPush       int
}

// DefaultPolicy mirrors the doc defaults.
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

// InDND reports whether t falls inside the user's quiet window.
func (p Policy) InDND(t time.Time, loc *time.Location) bool {
	h := t.In(loc).Hour()
	if p.DNDStartHour > p.DNDEndHour { // wraps midnight, e.g. 22→7
		return h >= p.DNDStartHour || h < p.DNDEndHour
	}
	return h >= p.DNDStartHour && h < p.DNDEndHour
}

// DNDWindowEnd returns the next moment the quiet window opens for delivery.
func (p Policy) DNDWindowEnd(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	h := local.Hour()
	switch {
	case p.DNDStartHour > p.DNDEndHour:
		if h >= p.DNDStartHour {
			// inside evening block → tomorrow morning
			return time.Date(local.Year(), local.Month(), local.Day(), p.DNDEndHour, 0, 0, 0, loc).AddDate(0, 0, 1)
		}
		if h < p.DNDEndHour {
			// inside morning block → today
			return time.Date(local.Year(), local.Month(), local.Day(), p.DNDEndHour, 0, 0, 0, loc)
		}
	default:
		if h >= p.DNDStartHour {
			return time.Date(local.Year(), local.Month(), local.Day(), p.DNDEndHour, 0, 0, 0, loc).AddDate(0, 0, 1)
		}
	}
	return local
}

// AllowFire decides whether a reminder may fire at fireAt for this user.
// When deferred, deferTo carries the adjusted fire time.
func (p Policy) AllowFire(user *store.User, level int, fireAt time.Time) (allow bool, deferTo time.Time) {
	loc := loadLocation(user.TZ)
	if !p.InDND(fireAt, loc) {
		return true, fireAt
	}
	// Children: absolute night silence — nothing breaks the window.
	if user.UserType == store.UserChild && p.ChildNightSilence {
		return false, p.DNDWindowEnd(fireAt, loc)
	}
	// Urgent (max level) may break DND for configured user types.
	if level >= p.MaxLevel && p.UrgentBreaksDNDFor[user.UserType] {
		return true, fireAt
	}
	// Everything else defers to the window end.
	return false, p.DNDWindowEnd(fireAt, loc)
}

// NextLevel returns the escalation level after an unacked delivery.
func (p Policy) NextLevel(level int) (int, bool) {
	if level >= p.MaxLevel {
		return level, false
	}
	return level + 1, true
}

// ChannelForLevel maps a level to its delivery channel.
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

// OverPushBudget reports whether the user already hit today's non-urgent push cap.
func (p Policy) OverPushBudget(sentToday int) bool {
	return sentToday >= p.MaxDailyPush
}

func loadLocation(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.FixedZone(tz, 8*3600) // fall back to CST for Asia/Shanghai-style names
	}
	return loc
}
