package task

import (
	"fmt"
	"time"

	"goldenfinger/agent/internal/store"
)

// Reminder copy templates. P3 (意图≠事实):
//   - intent: only reminds "don't forget to do X" — NEVER triggers event templates.
//   - fact:   events (already booked) trigger lead-time event templates.
//   - alarm:  rings at abs_time with the label.
//   - note:   never fires.

// BuildReminderText renders one reminder's user-facing Chinese copy.
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

// eventTemplateText renders lead-time copy for confirmed facts (P3).
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

// FactLeadReminders returns lead-time offsets for fact kinds with an event template.
// Only facts (已发生/已订票) get lead-time reminders — never intents.
func FactLeadReminders(template string) []time.Duration {
	switch template {
	case "trip":
		return []time.Duration{-24 * time.Hour, -3 * time.Hour}
	case "appointment":
		return []time.Duration{-2 * time.Hour, -30 * time.Minute}
	case "medication":
		return nil // fires at abs_time only
	default:
		return nil
	}
}

// DedupeKey builds the idempotency key for a reminder slot.
// Format: task:{id}:L{level}:{fire_at_unix} (digests use digest:{user}:{date}).
func DedupeKey(taskID string, level int, fireAt time.Time) string {
	return fmt.Sprintf("task:%s:L%d:%d", taskID, level, fireAt.UTC().Unix())
}

// DigestDedupeKey builds the once-a-day digest key.
func DigestDedupeKey(userID string, day time.Time) string {
	return fmt.Sprintf("digest:%s:%04d-%02d-%02d", userID, day.Year(), day.Month(), day.Day())
}

// Title extracts the human title from a task payload.
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
