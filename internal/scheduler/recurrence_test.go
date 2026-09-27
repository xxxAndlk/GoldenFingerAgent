package scheduler

import (
	"context"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

// findPendingRecurrence 在任务的提醒列表中找续排的下一条（level=1 且 pending）。
func findPendingRecurrence(t *testing.T, ctx context.Context, repos *store.Repos, taskID string) *store.Reminder {
	t.Helper()
	reminders, err := repos.Reminders.ListByTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range reminders {
		if reminders[i].Level == 1 && reminders[i].State == store.ReminderPending {
			return &reminders[i]
		}
	}
	t.Fatalf("no pending level-1 recurrence reminder in %+v", reminders)
	return nil
}

// TestRecurrenceDailyReschedules 验证 daily 任务 fire 后自动排下一次：
// 新增 reminder fire_at ≈ 次日同一时刻且 state=pending，任务不置 done。
func TestRecurrenceDailyReschedules(t *testing.T) {
	fixed := time.Date(2026, 3, 5, 10, 0, 0, 0, cst) // 周四
	repos, sched, _, u := setup(t, func() time.Time { return fixed })
	ctx := context.Background()

	fireAt := time.Date(2026, 3, 5, 9, 0, 0, 0, cst)
	taskRow := &store.Task{
		OwnerUserID: u.ID, Kind: store.KindIntent, Status: store.TaskScheduled,
		Schema: []byte(`{"title":"每日打卡","recurrence":"daily@09:00"}`), AbsTime: &fireAt,
	}
	if err := repos.Tasks.Create(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	if err := sched.EnqueueForTask(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	next := findPendingRecurrence(t, ctx, repos, taskRow.ID)
	want := time.Date(2026, 3, 6, 9, 0, 0, 0, cst) // 次日同一时刻
	if !next.FireAt.Equal(want) {
		t.Errorf("daily next fire_at = %v, want %v", next.FireAt, want)
	}

	// 任务保持已通知，不提前置 done。
	got, err := repos.Tasks.Get(ctx, u.ID, taskRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.TaskNotified {
		t.Errorf("task status = %q, want %q", got.Status, store.TaskNotified)
	}

	// 幂等：再次 tick 不重复投递/不产生第二条续排。
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	reminders, err := repos.Reminders.ListByTask(ctx, taskRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, r := range reminders {
		if r.State == store.ReminderPending && r.Level == 1 {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("pending level-1 reminders = %d, want 1", pending)
	}
}

// TestRecurrenceWeeklyReschedules 验证 weekly 任务 fire 后排到下个对应星期。
func TestRecurrenceWeeklyReschedules(t *testing.T) {
	fixed := time.Date(2026, 3, 5, 10, 0, 0, 0, cst) // 周四
	repos, sched, _, u := setup(t, func() time.Time { return fixed })
	ctx := context.Background()

	fireAt := time.Date(2026, 3, 5, 9, 0, 0, 0, cst)
	taskRow := &store.Task{
		OwnerUserID: u.ID, Kind: store.KindIntent, Status: store.TaskScheduled,
		Schema: []byte(`{"title":"周会","recurrence":"weekly@W3T09:00"}`), AbsTime: &fireAt,
	}
	if err := repos.Tasks.Create(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	if err := sched.EnqueueForTask(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	next := findPendingRecurrence(t, ctx, repos, taskRow.ID)
	want := time.Date(2026, 3, 11, 9, 0, 0, 0, cst) // 下周三
	if !next.FireAt.Equal(want) {
		t.Errorf("weekly next fire_at = %v, want %v", next.FireAt, want)
	}
}
