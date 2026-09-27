package task

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// 任务 schema 中承载周期表达式的字段（与 create_task 的
// time_expr_raw 语义对齐：daily@HH:MM / weekly@WnTHH:MM）。
const SchemaRecurrenceKey = "recurrence"

var (
	reDaily  = regexp.MustCompile(`^daily@(\d{1,2}):(\d{2})$`)
	reWeekly = regexp.MustCompile(`^weekly@W(\d)T(\d{1,2}):(\d{2})$`)
)

// Recurrence 从任务 schema 读取周期表达式（无周期返回空串）。
func Recurrence(t *store.Task) string {
	if t == nil || len(t.Schema) == 0 {
		return ""
	}
	var s map[string]any
	if err := json.Unmarshal(t.Schema, &s); err != nil {
		return ""
	}
	rec, _ := s[SchemaRecurrenceKey].(string)
	return rec
}

// NextOccurrence 依据周期表达式计算 from 之后的下一次触发时间。
// 支持 daily@HH:MM（次日同一时刻）与 weekly@WnTHH:MM（下个对应
// 星期，W1=周一…W7=周日）。loc 用于解析时刻；返回时间带 loc。
func NextOccurrence(rec string, from time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = from.Location()
	}
	if m := reDaily.FindStringSubmatch(rec); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		next := time.Date(from.Year(), from.Month(), from.Day(), h, min, 0, 0, loc)
		next = next.AddDate(0, 0, 1) // 同日规则：次日同一时刻
		return next, true
	}
	if m := reWeekly.FindStringSubmatch(rec); m != nil {
		iso, _ := strconv.Atoi(m[1])
		if iso < 1 || iso > 7 {
			return time.Time{}, false
		}
		h, _ := strconv.Atoi(m[2])
		min, _ := strconv.Atoi(m[3])
		target := time.Weekday(iso % 7) // W7=周日 → 0
		next := time.Date(from.Year(), from.Month(), from.Day(), h, min, 0, 0, loc)
		// 前进到下一个目标星期（严格晚于 from 当日 00:00）。
		days := (int(target) - int(next.Weekday()) + 7) % 7
		if days == 0 {
			days = 7
		}
		next = next.AddDate(0, 0, days)
		return next, true
	}
	return time.Time{}, false
}

// RecurrenceDedupeKey 为续排的下一次提醒构造幂等键，避免与历史
// 提醒的唯一键冲突（沿用 DedupeKey 格式）。
func RecurrenceDedupeKey(taskID string, level int, fireAt time.Time) string {
	return fmt.Sprintf("task:%s:R%d:%d", taskID, level, fireAt.UTC().Unix())
}

// NormalizeRecurrence 校验周期表达式格式（服务端防御，LLM 可能给脏数据）。
func NormalizeRecurrence(rec string) string {
	rec = strings.TrimSpace(rec)
	if rec == "" {
		return ""
	}
	if reDaily.MatchString(rec) || reWeekly.MatchString(rec) {
		return rec
	}
	return ""
}
