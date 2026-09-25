package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

// handleMemoryExport renders the user's whole memory as human-readable
// Markdown (USER.md/MEMORY.md spirit — no vector-soup black box: the user can
// read, audit and hand-edit what the butler knows).
func (s *Server) handleMemoryExport(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	var b strings.Builder

	name := u.Name
	if name == "" {
		name = "用户"
	}
	fmt.Fprintf(&b, "# 记忆档案 · %s\n\n", name)
	fmt.Fprintf(&b, "导出时间：%s ｜ 用户类型：%s\n\n", s.Now().Format("2006-01-02 15:04"), u.UserType)

	// ---- persons & facts ----
	b.WriteString("## 认识的人\n\n")
	persons, _ := s.Repos.Persons.ListByOwner(ctx, u.ID)
	if len(persons) == 0 {
		b.WriteString("（还没有人物记录）\n\n")
	}
	for _, p := range persons {
		fmt.Fprintf(&b, "### %s\n", p.CanonicalName)
		if aliases, err := s.Repos.Persons.ListAliases(ctx, p.ID); err == nil && len(aliases) > 0 {
			var names []string
			for _, a := range aliases {
				names = append(names, a.Alias)
			}
			fmt.Fprintf(&b, "- 别名：%s\n", strings.Join(names, "、"))
		}
		if p.Notes != "" {
			fmt.Fprintf(&b, "- 备注：%s\n", p.Notes)
		}
		if facts, err := s.Repos.Facts.ListByPerson(ctx, p.ID); err == nil {
			for _, f := range facts {
				mark := "已确认"
				if f.Status == store.FactInferred {
					mark = "推断，待确认"
				}
				fmt.Fprintf(&b, "- %s（%s，置信 %.2f）\n", f.ValueText, mark, f.Confidence)
			}
		}
		b.WriteString("\n")
	}

	// ---- tasks ----
	b.WriteString("## 任务与提醒\n\n")
	tasks, _ := s.Repos.Tasks.ListByOwner(ctx, u.ID, nil, "")
	if len(tasks) == 0 {
		b.WriteString("（还没有任务）\n\n")
	}
	for _, t := range tasks {
		when := "未定时间"
		if t.AbsTime != nil {
			when = t.AbsTime.In(loadLoc(u.TZ)).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "- [%s] %s（%s）\n", statusLabel(t.Status), task.Title(&t), when)
	}
	b.WriteString("\n")

	// ---- standing intents ----
	b.WriteString("## 常备提醒（事件触发）\n\n")
	intents, _ := s.Repos.Intents.ListByOwner(ctx, u.ID)
	if len(intents) == 0 {
		b.WriteString("（还没有事件触发的提醒）\n\n")
	}
	for _, it := range intents {
		var groups []string
		for _, g := range it.TriggerGroups {
			groups = append(groups, strings.Join(g, "且"))
		}
		exp := "长期"
		if it.ExpiresAt != nil {
			exp = "有效至 " + it.ExpiresAt.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "- 当「%s」出现时 → 提醒：%s（%s，已触发 %d/%d 次，%s）\n",
			strings.Join(groups, "，或"), it.Description, statusLabel(it.Status), it.FireCount, it.MaxFires, exp)
	}
	b.WriteString("\n")

	b.WriteString("---\n\n本档案由金手指管家生成，可在记忆面板逐条遗忘；删除后不可恢复。\n")

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=memory-dossier.md")
	_, _ = w.Write([]byte(b.String()))
}

func statusLabel(s string) string {
	switch s {
	case store.TaskDraft:
		return "草稿"
	case store.TaskPendingConfirm:
		return "待确认"
	case store.TaskScheduled:
		return "已排定"
	case store.TaskNotified:
		return "已提醒"
	case store.TaskDone: // == IntentDone
		return "已完成"
	case store.TaskSnoozed:
		return "已推迟"
	case store.TaskCancelled: // == IntentCancelled
		return "已取消"
	case store.TaskExpired: // == IntentExpired
		return "已过期"
	case store.IntentArmed:
		return "生效中"
	case store.IntentFired:
		return "已触发"
	}
	return s
}

func loadLoc(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.Local
}

// ---- standing intents REST (panel management) ----

func (s *Server) handleListIntents(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.Repos.Intents.ListByOwner(r.Context(), u.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) handleCancelIntent(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	if _, err := s.Repos.Intents.Cancel(r.Context(), u.ID, id); err != nil {
		writeError(w, 404, "intent not found or already closed")
		return
	}
	owner := u.ID
	_ = s.Repos.Audit.Append(r.Context(), &owner, "user", "intent_cancel", id, nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}
