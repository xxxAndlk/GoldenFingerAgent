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

// Queue 抽象了提醒调度，使领域层绝不直接与调度器实现对话
// （后续可以接入 Temporal Cloud）。
type Queue interface {
	EnqueueForTask(ctx context.Context, t *store.Task) error
	CancelForTask(ctx context.Context, taskID string) error
}

// QueueFunc 把普通函数适配为 Queue 接口（装配便利）。
type QueueFunc struct {
	Enq func(ctx context.Context, t *store.Task) error
	Can func(ctx context.Context, taskID string) error
}

func (q QueueFunc) EnqueueForTask(ctx context.Context, t *store.Task) error { return q.Enq(ctx, t) }
func (q QueueFunc) CancelForTask(ctx context.Context, taskID string) error  { return q.Can(ctx, taskID) }

// Service 是任务领域服务（create/confirm/done/snooze/cancel/expire）。
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

// CreateInput 是规范化的任务创建请求。
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
		AutoAccept     bool // >= task_auto 阈值 → 直接排定（可撤销）
	}

// Create 按门禁插入一条 draft/pending_confirm/scheduled 任务，然后
// 为已接受的任务入队提醒。
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

// Confirm 把 pending_confirm → scheduled 并入队提醒。
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

// Done 关闭任务并取消待发的提醒。
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

// Cancel 取消任务及其待发的提醒。
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

// Snooze 重新排定任务（scheduled/notified → snoozed → 带新时间的 scheduled）。
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

// MarkNotified 在提醒投递时把 scheduled → notified。
// 升级重提醒保持 notified 状态（notified → notified）。
func (s *Service) MarkNotified(ctx context.Context, taskID string) error {
	err := Transition(ctx, s.repo, taskID, store.TaskScheduled, store.TaskNotified, nil)
	if err == nil {
		return nil
	}
	return Transition(ctx, s.repo, taskID, store.TaskNotified, store.TaskNotified, nil)
}

// List 返回带可选过滤条件的任务。
func (s *Service) List(ctx context.Context, ownerID string, statuses []string, kind string) ([]store.Task, error) {
	return s.repo.ListByOwner(ctx, ownerID, statuses, kind)
}

// Get 取回一条任务。
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
