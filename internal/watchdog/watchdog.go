// Package watchdog 监视 Worker 心跳、重复循环、预算与未知副作用。
// 第一版只自动恢复只读幂等任务；有副作用的未知结果必须人工确认。
package watchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

type Watchdog struct {
	DB                  *store.DB
	Tasks               *worktask.Service
	Interval            time.Duration
	StaleAfter          time.Duration
	TaskBudget          time.Duration
	AutoRecoverReadOnly bool
	Now                 func() time.Time
	OnNotify            func(ownerID, body string)
}

func New(db *store.DB, tasks *worktask.Service) *Watchdog {
	return &Watchdog{
		DB: db, Tasks: tasks, Interval: 15 * time.Second, StaleAfter: 90 * time.Second,
		TaskBudget: 24 * time.Hour, AutoRecoverReadOnly: true, Now: time.Now,
	}
}

func (w *Watchdog) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watchdog) Start(ctx context.Context) {
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.ScanOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[watchdog] scan: %v", err)
				_ = w.healthEvent(ctx, err)
			}
		}
	}
}

func (w *Watchdog) ScanOnce(ctx context.Context) error {
	if w.DB == nil {
		return fmt.Errorf("watchdog: database not configured")
	}
	now := w.now()
	if err := w.interruptStale(ctx, now); err != nil {
		return err
	}
	tasks, err := store.NewRepos(w.DB).WorkTasks.ListNonTerminal(ctx, 200)
	if err != nil {
		return err
	}
	for i := range tasks {
		task := &tasks[i]
		if task.Status != store.WorkTaskActive {
			continue
		}
		if err := w.checkUnknown(ctx, task); err != nil {
			return err
		}
		if err := w.checkRepeatedLoop(ctx, task); err != nil {
			return err
		}
		if err := w.checkBudget(ctx, task, now); err != nil {
			return err
		}
	}
	return nil
}

func (w *Watchdog) interruptStale(ctx context.Context, now time.Time) error {
	jobs, err := store.NewRepos(w.DB).AgentJobs.InterruptStale(ctx, now.Add(-w.StaleAfter), now)
	if err != nil {
		return err
	}
	for i := range jobs {
		job := &jobs[i]
		body := "Worker 心跳超过 " + w.StaleAfter.String() + "，已标记 interrupted。任务：" + job.ID
		repos := store.NewRepos(w.DB)
		_ = repos.AgentJobs.AddEvent(ctx, job.ID, "interrupted", body, now)
		_, _ = repos.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: job.OwnerUserID, Kind: "watchdog.interrupted", EntityType: "worker_task",
			EntityID: job.ID, Payload: mustJSON(map[string]any{
				"work_task_id": job.WorkTaskID, "reason": "heartbeat_stale",
			}),
		})
		if job.WorkTaskID != "" && w.Tasks != nil {
			detail, getErr := w.Tasks.Get(ctx, job.OwnerUserID, job.WorkTaskID)
			if getErr == nil && detail.Task.Status != store.WorkTaskCancelled && detail.Task.Status != store.WorkTaskCompleted {
				_, _ = w.Tasks.Fail(ctx, job.OwnerUserID, job.WorkTaskID, job.ID, "Worker 心跳停滞")
				recovered := false
				if w.AutoRecoverReadOnly && detail.Task.Profile == store.WorkTaskProfileRead &&
					!hasUnknownInvocation(detail) {
					if _, recoverErr := w.Tasks.Recover(ctx, job.OwnerUserID, job.WorkTaskID, -1); recoverErr == nil {
						recovered = true
					}
				}
				message := body
				if recovered {
					message = "只读任务已从检查点创建新的执行段；旧 Job 保持 interrupted。"
				}
				w.notify(job.OwnerUserID, job.ID, message, now)
			}
		}
	}
	return nil
}

func (w *Watchdog) checkUnknown(ctx context.Context, task *store.WorkTask) error {
	invocations, err := store.NewRepos(w.DB).ToolInvocations.ListByTask(ctx, task.OwnerUserID, task.ID, 20)
	if err != nil {
		return err
	}
	for _, invocation := range invocations {
		if invocation.Status == store.ToolUnknown && invocation.CreatedAt.After(task.UpdatedAt) {
			return w.pauseTask(ctx, task, "检测到未确认副作用（unknown），请核对后再恢复", "unknown_effect")
		}
	}
	return nil
}

func (w *Watchdog) checkRepeatedLoop(ctx context.Context, task *store.WorkTask) error {
	invocations, err := store.NewRepos(w.DB).ToolInvocations.ListByTask(ctx, task.OwnerUserID, task.ID, 20)
	if err != nil || len(invocations) < 3 {
		return err
	}
	// ListByTask 按时间倒序；连续三次相同工具+参数 hash 才视为循环。
	first := invocations[0]
	for i := 1; i < 3; i++ {
		if invocations[i].ToolName != first.ToolName || invocations[i].ArgsHash != first.ArgsHash ||
			invocations[i].Status != store.ToolSuccess {
			return nil
		}
	}
	artifacts, err := store.NewRepos(w.DB).Artifacts.ListByTask(ctx, task.OwnerUserID, task.ID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if !artifact.UpdatedAt.Before(invocations[2].CreatedAt) {
			return nil
		}
	}
	return w.pauseTask(ctx, task, "检测到连续三次相同工具与参数且没有新交付物，已暂停等待你决定",
		"repeated_tool_loop")
}

func (w *Watchdog) checkBudget(ctx context.Context, task *store.WorkTask, now time.Time) error {
	if w.TaskBudget <= 0 || now.Sub(task.CreatedAt) < w.TaskBudget {
		return nil
	}
	return w.pauseTask(ctx, task, "任务超过时间预算，已暂停等待你决定是否继续", "budget_exceeded")
}

func (w *Watchdog) pauseTask(ctx context.Context, task *store.WorkTask, message, reason string) error {
	if w.Tasks == nil {
		return nil
	}
	updated, err := w.Tasks.Pause(ctx, task.OwnerUserID, task.ID, task.Version)
	if err != nil {
		return nil // 状态已被其他处理推进，不重复告警
	}
	repos := store.NewRepos(w.DB)
	_, _ = repos.AgentEvents.Append(ctx, &store.AgentEvent{
		OwnerUserID: task.OwnerUserID, Kind: "watchdog." + reason, EntityType: "work_task",
		EntityID: task.ID, Payload: mustJSON(map[string]any{"reason": reason, "status": updated.Status}),
	})
	w.notify(task.OwnerUserID, task.CurrentJobID, message, w.now())
	return nil
}

func (w *Watchdog) notify(ownerID, jobID, body string, at time.Time) {
	if ownerID == "" {
		return
	}
	if jobID != "" {
		_ = store.NewRepos(w.DB).AgentNotifications.Add(context.Background(), ownerID, jobID, body, at)
	}
	if w.OnNotify != nil {
		w.OnNotify(ownerID, body)
	}
}

func (w *Watchdog) healthEvent(ctx context.Context, err error) error {
	repos := store.NewRepos(w.DB)
	users, listErr := repos.Users.ListAll(ctx)
	if listErr != nil {
		return listErr
	}
	for _, user := range users {
		_, _ = repos.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: user.ID, Kind: "watchdog.health", EntityType: "system",
			EntityID: "watchdog", Payload: mustJSON(map[string]any{"error": err.Error()}),
		})
	}
	return nil
}

func hasUnknownInvocation(detail *worktask.Detail) bool {
	if detail == nil {
		return false
	}
	for _, invocation := range detail.Invocations {
		if invocation.Status == store.ToolUnknown {
			return true
		}
	}
	return false
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
