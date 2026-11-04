package watchdog_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/watchdog"
	"goldenfinger/agent/internal/worktask"
)

func watchdogFixture(t *testing.T) (*store.DB, *worktask.Service, *store.User) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	u := &store.User{UserType: store.UserGeneral, Name: "watchdog-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	return db, worktask.New(db), u
}

func TestWatchdogInterruptsStaleJob(t *testing.T) {
	db, tasks, u := watchdogFixture(t)
	ctx := context.Background()
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: u.ID, Title: "执行", Goal: "执行任务", IdempotencyKey: "stale-task", Start: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	job, _ := repos.AgentJobs.ActivePrimaryForTask(ctx, u.ID, task.ID)
	if _, err := repos.AgentJobs.MarkRunning(ctx, job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_job SET heartbeat_at = ? WHERE id = ?`,
		time.Now().Add(-5*time.Minute), job.ID); err != nil {
		t.Fatal(err)
	}
	wd := watchdog.New(db, tasks)
	wd.StaleAfter = 90 * time.Second
	wd.AutoRecoverReadOnly = false
	wd.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if err := wd.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := repos.AgentJobs.Get(ctx, u.ID, job.ID)
	if got.Status != store.AgentJobInterrupted {
		t.Fatalf("job status=%s", got.Status)
	}
	detail, _ := tasks.Get(ctx, u.ID, task.ID)
	if detail.Task.Status != store.WorkTaskFailed {
		t.Fatalf("task status=%s", detail.Task.Status)
	}
	notes, _ := repos.AgentNotifications.Unread(ctx, u.ID, 0, 10)
	if len(notes) == 0 {
		t.Fatal("watchdog must notify")
	}
}

func TestWatchdogAutoRecoversOnlyReadOnlyTask(t *testing.T) {
	db, tasks, u := watchdogFixture(t)
	ctx := context.Background()
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: u.ID, Title: "研读", Goal: "阅读资料", Profile: store.WorkTaskProfileRead,
		IdempotencyKey: "recover-read", Start: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	job, _ := repos.AgentJobs.ActivePrimaryForTask(ctx, u.ID, task.ID)
	if _, err := repos.AgentJobs.MarkRunning(ctx, job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_job SET heartbeat_at = ? WHERE id = ?`,
		time.Now().Add(-5*time.Minute), job.ID); err != nil {
		t.Fatal(err)
	}
	wd := watchdog.New(db, tasks)
	wd.StaleAfter = 90 * time.Second
	wd.AutoRecoverReadOnly = true
	wd.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if err := wd.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	detail, _ := tasks.Get(ctx, u.ID, task.ID)
	if detail.Task.Status != store.WorkTaskQueued {
		t.Fatalf("read-only task should be queued for recovery: %s", detail.Task.Status)
	}
	active, _ := repos.AgentJobs.ActivePrimaryForTask(ctx, u.ID, task.ID)
	if active == nil || active.ID == job.ID {
		t.Fatalf("recovery must create a new job: %+v", active)
	}
}

func TestWatchdogPausesRepeatedLoopAndUnknownEffect(t *testing.T) {
	db, tasks, u := watchdogFixture(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	task := &store.WorkTask{
		OwnerUserID: u.ID, Title: "循环", Goal: "执行", Status: store.WorkTaskActive,
		Profile: store.WorkTaskProfileInternal, IdempotencyKey: "loop-task",
	}
	if err := repos.WorkTasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		inv := &store.ToolInvocation{
			OwnerUserID: u.ID, TaskID: task.ID, ToolName: "bash", ArgsHash: "same",
			IdempotencyKey: "inv-" + uuid.NewString(), Status: store.ToolSuccess,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := repos.ToolInvocations.Create(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	wd := watchdog.New(db, tasks)
	wd.AutoRecoverReadOnly = false
	if err := wd.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	detail, _ := tasks.Get(ctx, u.ID, task.ID)
	if detail.Task.Status != store.WorkTaskPaused {
		t.Fatalf("repeated loop must pause task: %s", detail.Task.Status)
	}

	other := &store.WorkTask{
		OwnerUserID: u.ID, Title: "未知", Goal: "外部动作", Status: store.WorkTaskActive,
		Profile: store.WorkTaskProfileExternal, IdempotencyKey: "unknown-task",
	}
	if err := repos.WorkTasks.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	// Task 更新时间早于 unknown invocation，Watchdog 才能判定为当前执行段产生。
	if _, err := db.ExecContext(ctx, `UPDATE work_task SET updated_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute), other.ID); err != nil {
		t.Fatal(err)
	}
	if err := repos.ToolInvocations.Create(ctx, &store.ToolInvocation{
		OwnerUserID: u.ID, TaskID: other.ID, ToolName: "send_email", ArgsHash: "x",
		IdempotencyKey: "unknown-inv", Status: store.ToolUnknown, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := wd.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	detail, _ = tasks.Get(ctx, u.ID, other.ID)
	if detail.Task.Status != store.WorkTaskPaused {
		t.Fatalf("unknown effect must pause task: %s", detail.Task.Status)
	}
}
