package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

// Scheduler abstracts reminder scheduling. The DB tick loop implements it
// now; Temporal Cloud can implement the same methods later.
type Scheduler interface {
	Start(ctx context.Context) error // blocks until ctx is cancelled
	EnqueueForTask(ctx context.Context, t *store.Task) error
	CancelForTask(ctx context.Context, taskID string) error
	Enqueue(ctx context.Context, r *store.Reminder) (bool, error)
}

// Dispatcher delivers a reminder body over a channel (app/push/sms/voice).
type Dispatcher interface {
	Deliver(ctx context.Context, u *store.User, r *store.Reminder, body string) error
}

// DeliverFunc adapts a plain func to the Dispatcher interface.
type DeliverFunc func(ctx context.Context, u *store.User, r *store.Reminder, body string) error

func (f DeliverFunc) Deliver(ctx context.Context, u *store.User, r *store.Reminder, body string) error {
	return f(ctx, u, r, body)
}

// DBScheduler is the in-process persistent tick loop (doc §5.5 备选 path).
type DBScheduler struct {
	repos    *store.Repos
	policy   Policy
	dispatch Dispatcher
	tasks    *task.Service
	tick     time.Duration
	digestAt string // "11:00" user-local
	now      func() time.Time
	look     time.Duration // fire slightly ahead to allow DND deferral bookkeeping
}

func New(repos *store.Repos, policy Policy, dispatch Dispatcher, tasks *task.Service, tick time.Duration, digestAt string, now func() time.Time) *DBScheduler {
	if now == nil {
		now = time.Now
	}
	if tick == 0 {
		tick = 30 * time.Second
	}
	return &DBScheduler{
		repos: repos, policy: policy, dispatch: dispatch, tasks: tasks,
		tick: tick, digestAt: digestAt, now: now, look: time.Minute,
	}
}

// Start runs the tick loop until ctx is cancelled.
func (s *DBScheduler) Start(ctx context.Context) error {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.Tick(ctx); err != nil {
				log.Printf("scheduler tick: %v", err)
			}
		}
	}
}

// Tick processes due reminders, expirations and digests once (exposed for tests).
func (s *DBScheduler) Tick(ctx context.Context) error {
	now := s.now()
	due, err := s.repos.Reminders.Due(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, r := range due {
		if err := s.fire(ctx, r, now); err != nil {
			log.Printf("scheduler fire %s: %v", r.ID, err)
		}
	}
	// Expire overdue tasks.
	expired, err := s.repos.Tasks.ExpireOverdue(ctx, now)
	if err != nil {
		return err
	}
	for _, t := range expired {
		log.Printf("[sched] expire task=%s title=%q", t.ID, task.Title(&t))
		owner := t.OwnerUserID
		_ = s.repos.Audit.Append(ctx, &owner, "scheduler", "task_expire", t.ID, nil)
		_ = s.repos.Reminders.CancelPendingByTask(ctx, t.ID)
	}
	return s.runDigests(ctx, now)
}

// EnqueueForTask builds reminder rows for a task from its kind/template.
func (s *DBScheduler) EnqueueForTask(ctx context.Context, t *store.Task) error {
	if t.AbsTime == nil {
		return nil // notes / undated tasks never fire
	}
	user, err := s.repos.Users.Get(ctx, t.OwnerUserID)
	if err != nil {
		return err
	}
	loc := loadLocation(user.TZ)

	type slot struct {
		at    time.Time
		level int
	}
	slots := []slot{{at: *t.AbsTime, level: 1}}

	// Facts with event templates get lead-time reminders (P3 — intents never).
	if t.Kind == store.KindFact {
		for _, lead := range task.FactLeadReminders(t.EventTemplate) {
			slots = append(slots, slot{at: t.AbsTime.Add(lead), level: 1})
		}
	}

	for _, sl := range slots {
		fireAt := sl.at
		if allow, deferTo := s.policy.AllowFire(user, sl.level, fireAt); !allow {
			fireAt = deferTo
		}
		ch := s.policy.ChannelForLevel(sl.level)
		rem := &store.Reminder{
			TaskID:    t.ID,
			FireAt:    fireAt,
			Channel:   ch,
			Level:     sl.level,
			DedupeKey: task.DedupeKey(t.ID, sl.level, sl.at),
		}
		if _, err := s.repos.Reminders.InsertIdempotent(ctx, rem); err != nil {
			return fmt.Errorf("enqueue reminder: %w", err)
		}
	}
	_ = loc
	return nil
}

// CancelForTask drops pending reminders (task done/snoozed/cancelled).
func (s *DBScheduler) CancelForTask(ctx context.Context, taskID string) error {
	return s.repos.Reminders.CancelPendingByTask(ctx, taskID)
}

// Enqueue inserts one reminder idempotently.
func (s *DBScheduler) Enqueue(ctx context.Context, r *store.Reminder) (bool, error) {
	return s.repos.Reminders.InsertIdempotent(ctx, r)
}

// fire delivers one due reminder and applies DND/escalation policy.
func (s *DBScheduler) fire(ctx context.Context, r store.Reminder, now time.Time) error {
	t, err := s.repos.Tasks.GetByID(ctx, r.TaskID)
	if err != nil {
		return s.repos.Reminders.Mark(ctx, r.ID, store.ReminderFailed)
	}
	user, err := s.repos.Users.Get(ctx, t.OwnerUserID)
	if err != nil {
		return s.repos.Reminders.Mark(ctx, r.ID, store.ReminderFailed)
	}

	allow, deferTo := s.policy.AllowFire(user, r.Level, r.FireAt)
	if !allow {
		// Defer: rewrite fire_at and keep pending (idempotency key unchanged).
		log.Printf("[sched] defer reminder=%s task=%q until=%s (DND/quiet hours)", r.ID, task.Title(t), deferTo.Format("01-02 15:04"))
		return s.repos.Reminders.Defer(ctx, r.ID, deferTo)
	}

	// Push budget: non-urgent over-cap coalesces into the digest instead.
	if r.Channel != "app" && r.Level < s.policy.MaxLevel {
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		sent, err := s.repos.Reminders.CountSentSince(ctx, user.ID, today, []string{"push", "sms"}, s.policy.MaxLevel-1)
		if err == nil && s.policy.OverPushBudget(sent) {
			log.Printf("[sched] swallow reminder=%s task=%q into digest (daily push budget)", r.ID, task.Title(t))
			return s.repos.Reminders.Mark(ctx, r.ID, store.ReminderSent) // swallowed into digest
		}
	}

	body := task.BuildReminderText(t, r.Level, task.Title(t))
	if err := s.dispatch.Deliver(ctx, user, &r, body); err != nil {
		return s.repos.Reminders.Mark(ctx, r.ID, store.ReminderFailed)
	}
	log.Printf("[sched] fire task=%q level=%d channel=%s fire_at=%s", task.Title(t), r.Level, r.Channel, r.FireAt.Format("01-02 15:04"))
	if err := s.repos.Reminders.Mark(ctx, r.ID, store.ReminderSent); err != nil {
		return err
	}
	if err := s.tasks.MarkNotified(ctx, t.ID); err != nil {
		log.Printf("scheduler: mark notified %s: %v", t.ID, err)
	}

	// Schedule the next escalation level if the task still isn't done.
	if next, ok := s.policy.NextLevel(r.Level); ok {
		esc := &store.Reminder{
			TaskID:    t.ID,
			FireAt:    r.FireAt.Add(s.policy.AckTimeout),
			Channel:   s.policy.ChannelForLevel(next),
			Level:     next,
			DedupeKey: task.DedupeKey(t.ID, next, r.FireAt.Add(s.policy.AckTimeout)),
		}
		log.Printf("[sched] escalate task=%q → level=%d at=%s", task.Title(t), next, esc.FireAt.Format("01-02 15:04"))
		_, _ = s.repos.Reminders.InsertIdempotent(ctx, esc)
	}
	return nil
}

// runDigests sends the 11:00 daily digest, only when a decision is needed.
func (s *DBScheduler) runDigests(ctx context.Context, now time.Time) error {
	users, err := s.repos.Users.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		loc := loadLocation(u.TZ)
		local := now.In(loc)
		hhmm := fmt.Sprintf("%02d:%02d", local.Hour(), local.Minute())
		if hhmm < s.digestAt || hhmm > addMinutes(s.digestAt, 5) {
			continue
		}
		if s.policy.InDND(now, loc) {
			continue // night silence applies to digests too
		}
		if err := s.sendDigest(ctx, u, local); err != nil {
			log.Printf("scheduler digest %s: %v", u.ID, err)
		}
	}
	return nil
}

func addMinutes(hhmm string, min int) string {
	var h, m int
	_, _ = fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	total := h*60 + m + min
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

// sendDigest builds and delivers the daily digest when there is something to decide.
// Idempotency: one digest per user per day, guarded by the dedupe key recorded
// in audit_log (checked before send; the tick loop is single-threaded).
func (s *DBScheduler) sendDigest(ctx context.Context, u store.User, local time.Time) error {
	key := task.DigestDedupeKey(u.ID, local)
	sent, err := s.repos.Audit.Exists(ctx, u.ID, "digest_send", key)
	if err != nil {
		return err
	}
	if sent {
		return nil
	}
	pending, err := s.repos.Tasks.ListByOwner(ctx, u.ID, []string{store.TaskPendingConfirm}, "")
	if err != nil {
		return err
	}
	scheduled, err := s.repos.Tasks.ListByOwner(ctx, u.ID, []string{store.TaskScheduled, store.TaskNotified}, "")
	if err != nil {
		return err
	}
	// Only bother the user when a decision is needed (doc F3).
	var lines []string
	for _, t := range pending {
		lines = append(lines, fmt.Sprintf("· 待确认：%s", task.Title(&t)))
	}
	for _, t := range scheduled {
		if t.Deadline != nil && t.Deadline.Before(local.Add(24*time.Hour)) {
			lines = append(lines, fmt.Sprintf("· 今天截止：%s", task.Title(&t)))
		}
	}
	if len(lines) == 0 {
		return nil // 仅在需决策时打扰
	}
	body := "📋 今日盘点\n" + joinLines(lines) + "\n需要我帮你处理哪一条吗？"
	log.Printf("[sched] digest user=%s items=%d", u.ID, len(lines))

	if s.dispatch != nil {
		fake := &store.Reminder{FireAt: local, Channel: "app", Level: 1, DedupeKey: key}
		if err := s.dispatch.Deliver(ctx, &u, fake, body); err != nil {
			return err
		}
	}
	owner := u.ID
	return s.repos.Audit.Append(ctx, &owner, "scheduler", "digest_send", key, nil)
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
