package scheduler

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

type recordingDispatcher struct {
	mu   sync.Mutex
	sent []string
}

func (d *recordingDispatcher) Deliver(ctx context.Context, u *store.User, r *store.Reminder, body string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, body)
	return nil
}

func (d *recordingDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sent)
}

func setup(t *testing.T, now func() time.Time) (*store.Repos, *DBScheduler, *recordingDispatcher, *store.User) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping scheduler integration tests")
	}
	ctx := context.Background()
	db, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db.Pool)

	u := &store.User{UserType: store.UserGeneral, Name: "sched-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	dispatch := &recordingDispatcher{}
	policy := DefaultPolicy()
	var sched *DBScheduler
	tasksSvc := task.NewService(repos.Tasks, task.QueueFunc{
		Enq: func(ctx context.Context, tk *store.Task) error { return sched.EnqueueForTask(ctx, tk) },
		Can: func(ctx context.Context, id string) error { return sched.CancelForTask(ctx, id) },
	}, repos.Audit, now)
	sched = New(repos, policy, dispatch, tasksSvc, time.Second, "11:00", now)
	return repos, sched, dispatch, u
}

func TestFireDeliversAndIsIdempotent(t *testing.T) {
	fixed := time.Date(2026, 3, 5, 10, 0, 0, 0, cst)
	repos, sched, dispatch, u := setup(t, func() time.Time { return fixed })
	ctx := context.Background()

	taskRow := &store.Task{
		OwnerUserID: u.ID, Kind: store.KindIntent, Status: store.TaskScheduled,
		Schema: []byte(`{"title":"订票"}`),
	}
	if err := repos.Tasks.Create(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	rem := &store.Reminder{
		TaskID: taskRow.ID, FireAt: fixed.Add(-time.Minute), Channel: "app", Level: 1,
		DedupeKey: "task:" + taskRow.ID + ":L1:1000",
	}
	if _, err := repos.Reminders.InsertIdempotent(ctx, rem); err != nil {
		t.Fatal(err)
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if dispatch.count() != 1 {
		t.Fatalf("want 1 delivery, got %d", dispatch.count())
	}

	// Second tick: the reminder is already sent — no double-send.
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if dispatch.count() != 1 {
		t.Fatalf("double send! got %d", dispatch.count())
	}

	// Task should now be notified.
	got, err := repos.Tasks.Get(ctx, u.ID, taskRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.TaskNotified {
		t.Errorf("task status = %q, want notified", got.Status)
	}

	// Escalation reminder queued (level 2 after ack timeout).
	reminders, err := repos.Reminders.ListByTask(ctx, taskRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasLevel2 := false
	for _, r := range reminders {
		if r.Level == 2 {
			hasLevel2 = true
		}
	}
	if !hasLevel2 {
		t.Error("expected a level-2 escalation reminder")
	}
}

func TestDNDDeferralOnEnqueue(t *testing.T) {
	// Now is daytime; the task fires at 23:00 (inside DND) → deferred to 07:00.
	fixed := time.Date(2026, 3, 5, 10, 0, 0, 0, cst)
	repos, sched, _, u := setup(t, func() time.Time { return fixed })
	ctx := context.Background()

	fireAt := time.Date(2026, 3, 5, 23, 0, 0, 0, cst)
	taskRow := &store.Task{
		OwnerUserID: u.ID, Kind: store.KindIntent, Status: store.TaskScheduled,
		Schema: []byte(`{"title":"夜间任务"}`), AbsTime: &fireAt,
	}
	if err := repos.Tasks.Create(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	if err := sched.EnqueueForTask(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	reminders, err := repos.Reminders.ListByTask(ctx, taskRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 {
		t.Fatalf("want 1 reminder, got %d", len(reminders))
	}
	want := time.Date(2026, 3, 6, 7, 0, 0, 0, cst)
	if !reminders[0].FireAt.Equal(want) {
		t.Errorf("fire_at = %v, want deferred to %v", reminders[0].FireAt, want)
	}
}

func TestEnqueueIdempotentAcrossRestarts(t *testing.T) {
	fixed := time.Date(2026, 3, 5, 10, 0, 0, 0, cst)
	repos, sched, _, u := setup(t, func() time.Time { return fixed })
	ctx := context.Background()

	fireAt := fixed.Add(2 * time.Hour)
	taskRow := &store.Task{
		OwnerUserID: u.ID, Kind: store.KindAlarm, Status: store.TaskScheduled,
		Schema: []byte(`{"title":"吃药"}`), AbsTime: &fireAt,
	}
	if err := repos.Tasks.Create(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	// Simulate a restart: two scheduler instances enqueue the same task.
	if err := sched.EnqueueForTask(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	sched2 := New(repos, DefaultPolicy(), &recordingDispatcher{}, nil, time.Second, "11:00", func() time.Time { return fixed })
	if err := sched2.EnqueueForTask(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	reminders, err := repos.Reminders.ListByTask(ctx, taskRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 {
		t.Fatalf("restart must not duplicate reminders, got %d", len(reminders))
	}
}

func TestCancelClearsPendingReminders(t *testing.T) {
	fixed := time.Date(2026, 3, 5, 10, 0, 0, 0, cst)
	repos, sched, _, u := setup(t, func() time.Time { return fixed })
	ctx := context.Background()

	fireAt := fixed.Add(2 * time.Hour)
	taskRow := &store.Task{
		OwnerUserID: u.ID, Kind: store.KindAlarm, Status: store.TaskScheduled,
		Schema: []byte(`{"title":"取消我"}`), AbsTime: &fireAt,
	}
	if err := repos.Tasks.Create(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	if err := sched.EnqueueForTask(ctx, taskRow); err != nil {
		t.Fatal(err)
	}
	if err := sched.CancelForTask(ctx, taskRow.ID); err != nil {
		t.Fatal(err)
	}
	reminders, err := repos.Reminders.ListByTask(ctx, taskRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reminders {
		if r.State == store.ReminderPending {
			t.Error("pending reminders must be cancelled with the task")
		}
	}
}
