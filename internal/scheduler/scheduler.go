package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

// Scheduler 抽象提醒调度。目前由基于数据库的 tick 循环实现；
// 日后 Temporal Cloud 可以实现同样的方法。
type Scheduler interface {
	Start(ctx context.Context) error // 阻塞直到 ctx 被取消
	EnqueueForTask(ctx context.Context, t *store.Task) error
	CancelForTask(ctx context.Context, taskID string) error
	Enqueue(ctx context.Context, r *store.Reminder) (bool, error)
}

// Dispatcher 通过某个渠道（app/push/sms/voice）投递提醒正文。
type Dispatcher interface {
	Deliver(ctx context.Context, u *store.User, r *store.Reminder, body string) error
}

// DeliverFunc 把普通函数适配为 Dispatcher 接口。
type DeliverFunc func(ctx context.Context, u *store.User, r *store.Reminder, body string) error

func (f DeliverFunc) Deliver(ctx context.Context, u *store.User, r *store.Reminder, body string) error {
	return f(ctx, u, r, body)
}

// DBScheduler 是进程内的持久化 tick 循环（文档 §5.5 备选路径）。
type DBScheduler struct {
	repos    *store.Repos
	policy   Policy
	dispatch Dispatcher
	tasks    *task.Service
	tick     time.Duration
	digestAt string // 用户本地时区的 "11:00"
	now      func() time.Time
	look     time.Duration // 略微提前触发，为免打扰延迟记账留出余地
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

// Start 运行 tick 循环直到 ctx 被取消。
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

// Tick 处理一次到期提醒、过期与摘要（为测试暴露）。
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
	// 过期未完成任务。
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

// EnqueueForTask 根据任务的类型/模板构建提醒行。
func (s *DBScheduler) EnqueueForTask(ctx context.Context, t *store.Task) error {
	if t.AbsTime == nil {
		return nil // 备忘/无日期任务永不触发
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

	// 带事件模板的事实获得提前量提醒（P3——意图永不提前）。
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

// CancelForTask 删除待处理提醒（任务完成/延后/取消）。
func (s *DBScheduler) CancelForTask(ctx context.Context, taskID string) error {
	return s.repos.Reminders.CancelPendingByTask(ctx, taskID)
}

// Enqueue 幂等地插入一条提醒。
func (s *DBScheduler) Enqueue(ctx context.Context, r *store.Reminder) (bool, error) {
	return s.repos.Reminders.InsertIdempotent(ctx, r)
}

// fire 投递一条到期提醒并应用免打扰/升级策略。
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
		// 延迟：重写 fire_at 并保持待处理（幂等键不变）。
		log.Printf("[sched] defer reminder=%s task=%q until=%s (DND/quiet hours)", r.ID, task.Title(t), deferTo.Format("01-02 15:04"))
		return s.repos.Reminders.Defer(ctx, r.ID, deferTo)
	}

	// 推送预算：超上限的非紧急提醒合并进摘要。
	if r.Channel != "app" && r.Level < s.policy.MaxLevel {
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		sent, err := s.repos.Reminders.CountSentSince(ctx, user.ID, today, []string{"push", "sms"}, s.policy.MaxLevel-1)
		if err == nil && s.policy.OverPushBudget(sent) {
			log.Printf("[sched] swallow reminder=%s task=%q into digest (daily push budget)", r.ID, task.Title(t))
			return s.repos.Reminders.Mark(ctx, r.ID, store.ReminderSent) // 已合并进摘要
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

	// 如果任务仍未完成，安排下一个升级级别。
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

// runDigests 发送 11:00 每日摘要，仅在需要决策时发送。
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
			continue // 夜间静默同样适用于摘要
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

// sendDigest 在有需要决策的事项时构建并投递每日摘要。
// 幂等性：每个用户每天一份摘要，由记入 audit_log 的去重键守护
// （发送前检查；tick 循环是单线程的）。
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
	// 待确认的推断记忆同样是需要决策的事项（记忆
	// 巩固回顾——"做梦"式询问主人的通道）。
	inferred, err := s.repos.Facts.ListInferredCurrent(ctx, u.ID)
	if err != nil {
		return err
	}
	// 仅在需要决策时才打扰用户（文档 F3）。
	var lines []string
	for _, t := range pending {
		lines = append(lines, fmt.Sprintf("· 待确认：%s", task.Title(&t)))
	}
	for _, t := range scheduled {
		if t.Deadline != nil && t.Deadline.Before(local.Add(24*time.Hour)) {
			lines = append(lines, fmt.Sprintf("· 今天截止：%s", task.Title(&t)))
		}
	}
	for _, f := range inferred {
		lines = append(lines, fmt.Sprintf("· 记忆待确认：%s —— %s（说「对」确认或「不对」纠正）", f.PersonName, f.ValueText))
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
