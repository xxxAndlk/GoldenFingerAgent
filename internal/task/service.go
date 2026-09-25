package task

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"goldenfinger/agent/internal/store"
)

func unmarshalSchema(raw json.RawMessage, v any) error { return json.Unmarshal(raw, v) }

// Queue abstracts reminder scheduling so the domain layer never talks to the
// scheduler implementation directly (Temporal Cloud can slot in later).
type Queue interface {
	EnqueueForTask(ctx context.Context, t *store.Task) error
	CancelForTask(ctx context.Context, taskID string) error
}

// QueueFunc adapts plain functions to the Queue interface (wiring convenience).
type QueueFunc struct {
	Enq func(ctx context.Context, t *store.Task) error
	Can func(ctx context.Context, taskID string) error
}

func (q QueueFunc) EnqueueForTask(ctx context.Context, t *store.Task) error { return q.Enq(ctx, t) }
func (q QueueFunc) CancelForTask(ctx context.Context, taskID string) error  { return q.Can(ctx, taskID) }

// Service is the task domain service (create/confirm/done/snooze/cancel/expire).
type Service struct {
	repo  *store.TaskRepo
	queue Queue
	audit *store.AuditRepo
	now   func() time.Time
}

func NewService(repo *store.TaskRepo, queue Queue, audit *store.AuditRepo, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, queue: queue, audit: audit, now: now}
}

// CreateInput is a normalized task creation request.
type CreateInput struct {
	OwnerUserID    string
	Kind           string // intent|fact|alarm|note
	Title          string
	TimeExprRaw    string
	AbsTime        *time.Time
	Deadline       *time.Time
	Confidence     float64
	SourceMsgID    *string
	LinkedPersonID *string
	EventTemplate  string
	AutoAccept     bool // >= task_auto threshold → scheduled directly (undoable)
}

// Create inserts a task in draft/pending_confirm/scheduled per gating, then
// enqueues reminders for accepted tasks.
func (s *Service) Create(ctx context.Context, in CreateInput) (*store.Task, error) {
	t := &store.Task{
		OwnerUserID:    in.OwnerUserID,
		Kind:           in.Kind,
		TimeExprRaw:    in.TimeExprRaw,
		AbsTime:        in.AbsTime,
		Deadline:       in.Deadline,
		Confidence:     in.Confidence,
		SourceMsgID:    in.SourceMsgID,
		LinkedPersonID: in.LinkedPersonID,
		EventTemplate:  in.EventTemplate,
	}
	payload, _ := json.Marshal(map[string]string{"title": in.Title})
	t.Schema = payload

	switch {
	case in.AutoAccept:
		t.Status = store.TaskScheduled
	default:
		t.Status = store.TaskPendingConfirm
	}

	if err := s.repo.Create(ctx, t); err != nil {
		return nil, err
	}
	log.Printf("[task] create id=%s kind=%s title=%q status=%s conf=%.2f abs=%v", t.ID, t.Kind, in.Title, t.Status, in.Confidence, t.AbsTime)
	s.auditAppend(ctx, in.OwnerUserID, actorAgent, "task_create", t.ID, map[string]any{
		"kind": in.Kind, "title": in.Title, "auto": in.AutoAccept, "confidence": in.Confidence,
	})
	if t.Status == store.TaskScheduled {
		if err := s.queue.EnqueueForTask(ctx, t); err != nil {
			return t, fmt.Errorf("task created but enqueue failed: %w", err)
		}
	}
	return t, nil
}

// Confirm moves pending_confirm → scheduled and enqueues reminders.
func (s *Service) Confirm(ctx context.Context, ownerID, id string) (*store.Task, error) {
	t, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	from := t.Status
	if err := Transition(ctx, s.repo, id, t.Status, store.TaskScheduled, nil); err != nil {
		return nil, err
	}
	t.Status = store.TaskScheduled
	log.Printf("[task] %s %q: %s → scheduled", id, Title(t), from)
	s.auditAppend(ctx, ownerID, actorUser, "task_confirm", id, nil)
	if err := s.queue.EnqueueForTask(ctx, t); err != nil {
		return t, err
	}
	return t, nil
}

// Done closes a task and cancels pending reminders.
func (s *Service) Done(ctx context.Context, ownerID, id string) error {
	t, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return err
	}
	if err := Transition(ctx, s.repo, id, t.Status, store.TaskDone, nil); err != nil {
		return err
	}
	log.Printf("[task] %s %q: %s → done", id, Title(t), t.Status)
	s.auditAppend(ctx, ownerID, actorUser, "task_done", id, nil)
	return s.queue.CancelForTask(ctx, id)
}

// Cancel cancels a task and its pending reminders.
func (s *Service) Cancel(ctx context.Context, ownerID, id string) error {
	t, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return err
	}
	if err := Transition(ctx, s.repo, id, t.Status, store.TaskCancelled, nil); err != nil {
		return err
	}
	log.Printf("[task] %s %q: %s → cancelled", id, Title(t), t.Status)
	s.auditAppend(ctx, ownerID, actorUser, "task_cancel", id, nil)
	return s.queue.CancelForTask(ctx, id)
}

// Snooze reschedules a task (scheduled/notified → snoozed → scheduled with new time).
func (s *Service) Snooze(ctx context.Context, ownerID, id string, newAbsTime time.Time) (*store.Task, error) {
	t, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	if err := Transition(ctx, s.repo, id, t.Status, store.TaskSnoozed, nil); err != nil {
		return nil, err
	}
	at := newAbsTime
	err = Transition(ctx, s.repo, id, store.TaskSnoozed, store.TaskScheduled, func(tk *store.Task) {
		tk.AbsTime = &at
	})
	if err != nil {
		return nil, err
	}
	t.Status = store.TaskScheduled
	t.AbsTime = &at
	log.Printf("[task] %s %q: snoozed → scheduled at %s", id, Title(t), at.Format("01-02 15:04"))
	s.auditAppend(ctx, ownerID, actorUser, "task_snooze", id, map[string]any{"abs_time": at})
	if err := s.queue.CancelForTask(ctx, id); err != nil {
		return nil, err
	}
	if err := s.queue.EnqueueForTask(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// MarkNotified flips scheduled → notified when a reminder is delivered.
// Escalation re-notifies keep the notified state (notified → notified).
func (s *Service) MarkNotified(ctx context.Context, taskID string) error {
	err := Transition(ctx, s.repo, taskID, store.TaskScheduled, store.TaskNotified, nil)
	if err == nil {
		return nil
	}
	return Transition(ctx, s.repo, taskID, store.TaskNotified, store.TaskNotified, nil)
}

// List returns tasks with optional filters.
func (s *Service) List(ctx context.Context, ownerID string, statuses []string, kind string) ([]store.Task, error) {
	return s.repo.ListByOwner(ctx, ownerID, statuses, kind)
}

// Get fetches one task.
func (s *Service) Get(ctx context.Context, ownerID, id string) (*store.Task, error) {
	return s.repo.Get(ctx, ownerID, id)
}

const (
	actorUser  = "user"
	actorAgent = "agent"
)

func (s *Service) auditAppend(ctx context.Context, ownerID, actor, action, target string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	owner := ownerID
	_ = s.audit.Append(ctx, &owner, actor, action, target, raw)
}
