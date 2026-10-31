package agenthub

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

// newJobTestDB 建一个真实 SQLite（临时文件），迁移到最新 schema。
func newJobTestDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func testOwner(t *testing.T, db *store.DB) string {
	t.Helper()
	ctx := context.Background()
	u := &store.User{UserType: "general", Name: "测试用户", TZ: "Asia/Shanghai"}
	if err := store.NewRepos(db).Users.Create(ctx, u); err != nil {
		t.Fatalf("建测试用户失败: %v", err)
	}
	return u.ID
}

// newJobTestDispatcher 返回未启动 Worker 的派发器（由用例决定何时 Start）。
func newJobTestDispatcher(t *testing.T, db *store.DB, delegate DelegateFunc) *Dispatcher {
	t.Helper()
	manager := New(t.TempDir(), "")
	d := NewDispatcher(db, manager)
	d.Delegate = delegate
	return d
}

func TestDispatcherSubmitRunsAndNotifies(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	ran := make(chan string, 1)
	d := newJobTestDispatcher(t, db, func(_ context.Context, agentID, task, _ string) (Invocation, error) {
		ran <- agentID + "|" + task
		return Invocation{State: "ok", Output: "整理完成，移动 12 个文件", TaskID: "ext-1"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}

	job, err := d.Submit(ctx, owner, "codex", "把下载目录按类型整理", "", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.AgentJobQueued {
		t.Fatalf("status=%s", job.Status)
	}
	select {
	case got := <-ran:
		if got != "codex|把下载目录按类型整理" {
			t.Fatalf("delegate got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delegate never ran")
	}

	final := waitJobStatus(t, d, owner, job.ID, store.AgentJobCompleted)
	if final.Output != "整理完成，移动 12 个文件" {
		t.Fatalf("output=%q", final.Output)
	}
	if final.ExternalTaskID != "ext-1" {
		t.Fatalf("external task id=%q", final.ExternalTaskID)
	}
	_, events, err := d.Get(ctx, owner, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 || events[len(events)-1].Type != store.AgentJobCompleted {
		t.Fatalf("events=%+v", events)
	}
	// 完成通知必须落库（手机端离线也不丢）。
	notes, err := store.NewRepos(db).AgentNotifications.Unread(ctx, owner, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "已完成") {
		t.Fatalf("notes=%+v", notes)
	}
}

func TestDispatcherIdempotencyKeyReturnsSameJob(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	release := make(chan struct{})
	d := newJobTestDispatcher(t, db, func(context.Context, string, string, string) (Invocation, error) {
		<-release
		return Invocation{State: "ok", Output: "done"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	first, err := d.Submit(ctx, owner, "codex", "跑测试", "", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Submit(ctx, owner, "codex", "跑测试", "", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("同幂等键应返回同一个任务: %s vs %s", first.ID, second.ID)
	}
}

func TestDispatcherCancelQueuedJob(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	block := make(chan struct{})
	defer close(block)
	d := newJobTestDispatcher(t, db, func(context.Context, string, string, string) (Invocation, error) {
		<-block
		return Invocation{State: "ok"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 一个占住 worker 的任务，让第二个任务停留在 queued。
	if _, err := d.Submit(ctx, owner, "codex", "长任务", "", "k-block"); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := d.Submit(ctx, owner, "codex", "排队中的任务", "", "k-queued")
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.Cancel(ctx, owner, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.AgentJobCancelled {
		t.Fatalf("status=%s", job.Status)
	}
	// 已结束任务不能再取消。
	if _, err := d.Cancel(ctx, owner, queued.ID); err == nil {
		t.Fatal("重复取消应报错")
	}
}

func TestDispatcherFailureRecordsError(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	d := newJobTestDispatcher(t, db, func(context.Context, string, string, string) (Invocation, error) {
		return Invocation{}, errors.New("agent 未配置")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := d.Submit(ctx, owner, "ghost", "任意任务", "", "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobStatus(t, d, owner, job.ID, store.AgentJobFailed)
	if !strings.Contains(final.Error, "agent 未配置") {
		t.Fatalf("error=%q", final.Error)
	}
}

func TestDispatcherTimeoutIsNotMisreportedAsCancelled(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	d := newJobTestDispatcher(t, db, func(ctx context.Context, _, _, _ string) (Invocation, error) {
		<-ctx.Done()
		return Invocation{}, ctx.Err()
	})
	d.Timeout = 80 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := d.Submit(ctx, owner, "codex", "跑到超时的任务", "", "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobStatus(t, d, owner, job.ID, store.AgentJobTimeout)
	if final.Status != store.AgentJobTimeout {
		t.Fatalf("status=%s", final.Status)
	}
}

func TestDispatcherCancelRunningJobStopsExecution(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	started := make(chan struct{})
	d := newJobTestDispatcher(t, db, func(ctx context.Context, _, _, _ string) (Invocation, error) {
		close(started)
		<-ctx.Done() // 只有被取消才会返回
		return Invocation{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := d.Submit(ctx, owner, "codex", "会被取消的长任务", "", "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := d.Cancel(ctx, owner, job.ID); err != nil {
		t.Fatal(err)
	}
	final := waitJobStatus(t, d, owner, job.ID, store.AgentJobCancelled)
	if final.Status != store.AgentJobCancelled {
		t.Fatalf("status=%s", final.Status)
	}
}

func TestDispatcherStartMarksRunningJobsInterrupted(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	ctx := context.Background()

	// 模拟上次进程崩溃：库里留下一个 running 任务。
	d0 := newJobTestDispatcher(t, db, func(context.Context, string, string, string) (Invocation, error) {
		return Invocation{State: "ok"}, nil
	})
	job, err := d0.Submit(ctx, owner, "codex", "崩溃前的任务", "", "k-crash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewRepos(db).AgentJobs.MarkRunning(ctx, job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	d := newJobTestDispatcher(t, db, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	final, _, err := d0.Get(ctx, owner, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.AgentJobInterrupted {
		t.Fatalf("status=%s（重启后不应盲目重跑）", final.Status)
	}
	if final.Output != "" {
		t.Fatalf("中断任务不应伪造输出: %q", final.Output)
	}
}

func TestDispatcherRejectsEmptyAndOversizeTask(t *testing.T) {
	db := newJobTestDB(t)
	owner := testOwner(t, db)
	d := newJobTestDispatcher(t, db, nil)
	ctx := context.Background()
	if _, err := d.Submit(ctx, owner, "codex", "   ", "", ""); err == nil {
		t.Fatal("空任务应被拒绝")
	}
	if _, err := d.Submit(ctx, owner, "codex", strings.Repeat("啊", 12001), "", ""); err == nil {
		t.Fatal("超长任务应被拒绝")
	}
}

func waitJobStatus(t *testing.T, d *Dispatcher, owner, id, want string) *store.AgentJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, _, err := d.Get(context.Background(), owner, id)
		if err == nil && job.Status == want {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, _, _ := d.Get(context.Background(), owner, id)
	if job != nil {
		t.Fatalf("任务未进入 %s，实际 %s（error=%q）", want, job.Status, job.Error)
	}
	t.Fatalf("任务 %s 未进入 %s", id, want)
	return nil
}
