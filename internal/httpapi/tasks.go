package httpapi

import (
	"net/http"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

type taskDTO struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	AbsTime    string  `json:"abs_time,omitempty"`
	Confidence float64 `json:"confidence"`
}

func toTaskDTO(t *store.Task, loc *time.Location) taskDTO {
	d := taskDTO{ID: t.ID, Title: task.Title(t), Kind: t.Kind, Status: t.Status, Confidence: t.Confidence}
	if t.AbsTime != nil {
		d.AbsTime = t.AbsTime.In(loc).Format("2006-01-02 15:04")
	}
	return d
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	status := r.URL.Query().Get("status")
	var statuses []string
	switch status {
	case "pending":
		statuses = []string{store.TaskPendingConfirm}
	case "upcoming":
		statuses = []string{store.TaskScheduled, store.TaskNotified, store.TaskSnoozed}
	case "done":
		statuses = []string{store.TaskDone}
	case "all":
		statuses = nil
	default:
		statuses = []string{store.TaskDraft, store.TaskPendingConfirm, store.TaskScheduled, store.TaskNotified, store.TaskSnoozed}
	}
	tasks, err := s.Repos.Tasks.ListByOwner(r.Context(), u.ID, statuses, "")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	loc := loadTZ(u.TZ)
	out := make([]taskDTO, 0, len(tasks))
	for i := range tasks {
		out = append(out, toTaskDTO(&tasks[i], loc))
	}
	writeJSON(w, 200, out)
}

// handleTaskAction wires the 【完成/推迟/取消】 reminder buttons to the state machine.
func (s *Server) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	action := r.PathValue("action")
	ctx := r.Context()

	switch action {
	case "done":
		if err := s.Runtime.Tools.Tasks.Done(ctx, u.ID, id); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	case "cancel":
		if err := s.Runtime.Tools.Tasks.Cancel(ctx, u.ID, id); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	case "snooze":
		// Default snooze: 30 minutes (UI sends no body for now).
		newTime := s.Runtime.Tools.Now().Add(30 * time.Minute)
		if _, err := s.Runtime.Tools.Tasks.Snooze(ctx, u.ID, id, newTime); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	case "confirm":
		if _, err := s.Runtime.Tools.Tasks.Confirm(ctx, u.ID, id); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	default:
		writeError(w, 400, "unknown action")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "action": action})
}

func (s *Server) handleReminders(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	// Surface upcoming task slots as the "reminder list" for the UI.
	tasks, err := s.Repos.Tasks.ListByOwner(r.Context(), u.ID,
		[]string{store.TaskScheduled, store.TaskNotified, store.TaskSnoozed}, "")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	type reminderDTO struct {
		TaskID  string `json:"task_id"`
		Title   string `json:"title"`
		FireAt  string `json:"fire_at"`
		Status  string `json:"status"`
		Actions []struct {
			Label string `json:"label"`
			Verb  string `json:"verb"`
		} `json:"actions"`
	}
	loc := loadTZ(u.TZ)
	out := make([]reminderDTO, 0, len(tasks))
	for _, t := range tasks {
		d := reminderDTO{TaskID: t.ID, Title: task.Title(&t), Status: t.Status}
		if t.AbsTime != nil {
			d.FireAt = t.AbsTime.In(loc).Format("01-02 15:04")
		}
		for _, lbl := range []struct{ Label, Verb string }{
			{"完成", "done"}, {"推迟", "snooze"}, {"取消", "cancel"},
		} {
			a := struct {
				Label string `json:"label"`
				Verb  string `json:"verb"`
			}{Label: lbl.Label, Verb: lbl.Verb}
			d.Actions = append(d.Actions, a)
		}
		out = append(out, d)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleOutbox(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items := s.Outbox.Drain(u.ID)
	if items == nil {
		items = []OutboxItem{}
	}
	writeJSON(w, 200, items)
}
