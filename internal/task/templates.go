package task

import (
	"fmt"
	"time"

	"goldenfinger/agent/internal/store"
)

// 提醒文案模板。P3（意图≠事实）：
//   - intent：只提醒“别忘做 X”——绝不触发事件模板。
//   - fact：  事件（已预订）触发提前量事件模板。
//   - alarm：  在 abs_time 以标签响铃。
//   - note：   永不触发。

// BuildReminderText 渲染一条提醒对用户的中文文案。
func BuildReminderText(t *store.Task, level int, title string) string {
	switch t.Kind {
	case store.KindAlarm:
		if level >= 2 {
			return fmt.Sprintf("⏰ 闹钟响啦：%s", title)
		}
		return fmt.Sprintf("⏰ 该开始了：%s", title)
	case store.KindIntent:
		return fmt.Sprintf("别忘了：%s", title)
	case store.KindFact:
		return eventTemplateText(t.EventTemplate, title)
	default:
		return title
	}
}

// eventTemplateText 为已确认的事实（P3）渲染提前量文案。
func eventTemplateText(template, title string) string {
	switch template {
	case "trip":
		return fmt.Sprintf("行程提醒：%s", title)
	case "appointment":
		return fmt.Sprintf("预约提醒：%s", title)
	case "medication":
		return fmt.Sprintf("用药提醒：%s", title)
	default:
		return fmt.Sprintf("提醒：%s", title)
	}
}

// FactLeadReminders 返回带事件模板的事实种类的提前量偏移。
// 只有事实（已发生/已订票）获得提前量提醒——意图绝不。
func FactLeadReminders(template string) []time.Duration {
	switch template {
	case "trip":
		return []time.Duration{-24 * time.Hour, -3 * time.Hour}
	case "appointment":
		return []time.Duration{-2 * time.Hour, -30 * time.Minute}
	case "medication":
		return nil // 只在 abs_time 触发
	default:
		return nil
	}
}

// DedupeKey 为提醒槽构建幂等键。
// 格式：task:{id}:L{level}:{fire_at_unix}（摘要使用 digest:{user}:{date}）。
func DedupeKey(taskID string, level int, fireAt time.Time) string {
	return fmt.Sprintf("task:%s:L%d:%d", taskID, level, fireAt.UTC().Unix())
}

// DigestDedupeKey 构建每天一次的摘要键。
func DigestDedupeKey(userID string, day time.Time) string {
	return fmt.Sprintf("digest:%s:%04d-%02d-%02d", userID, day.Year(), day.Month(), day.Day())
}

// Title 从任务负载中提取人类可读的标题。
func Title(t *store.Task) string {
	if t.Schema != nil {
		var s struct {
			Title string `json:"title"`
		}
		if err := unmarshalSchema(t.Schema, &s); err == nil && s.Title != "" {
			return s.Title
		}
	}
	if t.TimeExprRaw != "" {
		return t.TimeExprRaw
	}
	return "待办"
}
