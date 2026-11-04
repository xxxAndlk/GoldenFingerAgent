package superagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

// WorkerAgentID 标识由 TurnService 执行的 Super-agent Worker，
// 通用 CLI/BMA 队列会跳过该 agent_id。
const WorkerAgentID = store.AgentIDSuperAgent

type WorkerEvent struct {
	Kind   string
	Name   string
	Detail string
}

type WorkerRequest struct {
	OwnerID        string
	JobID          string
	TaskID         string
	ThreadID       string
	SessionID      string
	Goal           string
	Profile        string
	StepController agent.StepController
	ContextBlocks  []string
	OnEvent        func(WorkerEvent)
}

type WorkerResult struct {
	SessionID      string
	Reply          string
	Summary        string
	Paused         bool
	Cancelled      bool
	WaitingKind    string
	WaitingPayload json.RawMessage
}

type WorkerRunner interface {
	RunWorker(ctx context.Context, req WorkerRequest) (*WorkerResult, error)
}

type WorkerManager struct {
	DB     *store.DB
	Runner WorkerRunner
	// Tasks 非空时 create_worker 走 WorkTask 控制面；为空保持旧 Job 兼容路径。
	Tasks *worktask.Service
	// Artifacts 用于执行段结束时把草稿提交验收；为空时只使用结果摘要。
	Artifacts  *worktask.ArtifactService
	MaxWorkers int
	Timeout    time.Duration
	Now        func() time.Time
	// OnResult 在 Worker 成功完成后把提炼结果写入统一记忆。
	// 失败只记录日志，不改变已完成任务的状态。
	OnResult func(ctx context.Context, job *store.AgentJob, summary string) error
	// Context 为 WorkTask 生成任务级上下文块与 ContextReceipt；为空时只传 Goal。
	Context func(ctx context.Context, job *store.AgentJob) ([]string, json.RawMessage, error)

	mu     sync.Mutex
	root   context.Context
	wake   chan struct{}
	cancel map[string]context.CancelFunc
}

func NewWorkerManager(db *store.DB, runner WorkerRunner) *WorkerManager {
	return &WorkerManager{
		DB: db, Runner: runner, MaxWorkers: 3, Timeout: 10 * time.Minute, Now: time.Now,
		wake: make(chan struct{}, 1), cancel: make(map[string]context.CancelFunc),
	}
}

func (w *WorkerManager) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Start 启动固定数量的 Worker 循环；running 任务的启动恢复由通用 Dispatcher 统一处理。
func (w *WorkerManager) Start(ctx context.Context) error {
	if w.DB == nil {
		return errors.New("superagent: database not configured")
	}
	if w.Runner == nil {
		return errors.New("superagent: worker runner not configured")
	}
	if err := w.recoverInterrupted(ctx); err != nil {
		return err
	}
	w.mu.Lock()
	w.root = ctx
	n := w.MaxWorkers
	if n <= 0 {
		n = 3
	}
	w.mu.Unlock()
	for i := 0; i < n; i++ {
		go w.loop(ctx)
	}
	w.signal()
	return nil
}

// recoverInterrupted 只处理真正失去执行线程的 running Worker；
// waiting_kind != none 的任务保留等待状态，等审批/澄清回调恢复。
func (w *WorkerManager) recoverInterrupted(ctx context.Context) error {
	now := w.now()
	jobs, err := w.DB.AgentJobs().InterruptRunningForAgent(ctx, WorkerAgentID, now)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		body := "Worker 任务在程序重启前尚未确认结果，已保留记录，请核对后再决定是否重试。任务：" + job.ID
		if err := w.DB.WithTx(ctx, func(tx *sql.Tx) error {
			repos := store.NewRepos(tx)
			if err := repos.AgentJobs.AddEvent(ctx, job.ID, "interrupted", body, now); err != nil {
				return err
			}
			if err := appendEvent(ctx, repos, job.OwnerUserID, "worker.interrupted", "worker_task", job.ID, map[string]any{
				"thread_id": job.ThreadID, "trigger_id": job.ParentTriggerID, "status": store.AgentJobInterrupted,
			}); err != nil {
				return err
			}
			return repos.AgentNotifications.Add(ctx, job.OwnerUserID, job.ID, body, now)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (w *WorkerManager) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Wake 让主程序在 WorkTask 创建新执行段后立刻唤醒 Worker。
func (w *WorkerManager) Wake() { w.signal() }

// CancelJob 只取消正在执行的 context；数据库状态由 TaskService 在同一事务中结算。
func (w *WorkerManager) CancelJob(_ context.Context, _ string, _ string, jobID string) {
	w.mu.Lock()
	cancel := w.cancel[jobID]
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *WorkerManager) loop(ctx context.Context) {
	for {
		if err := w.runNext(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[superagent-worker] %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-time.After(2 * time.Second):
		}
	}
}

func (w *WorkerManager) runNext(ctx context.Context) error {
	job, err := w.claim(ctx)
	if err != nil || job == nil {
		return err
	}
	return w.runJob(ctx, job)
}

func (w *WorkerManager) claim(ctx context.Context) (*store.AgentJob, error) {
	var claimed *store.AgentJob
	now := w.now()
	err := w.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		next, err := repos.AgentJobs.FirstQueuedForAgent(ctx, WorkerAgentID)
		if err != nil || next == nil {
			return err
		}
		changed, err := repos.AgentJobs.MarkRunning(ctx, next.ID, now)
		if err != nil || !changed {
			return err
		}
		if err := repos.AgentJobs.AddEvent(ctx, next.ID, "running", "Worker 开始执行", now); err != nil {
			return err
		}
		if err := appendEvent(ctx, repos, next.OwnerUserID, "worker.running", "worker_task", next.ID, map[string]any{
			"thread_id": next.ThreadID, "trigger_id": next.ParentTriggerID, "status": store.AgentJobRunning,
		}); err != nil {
			return err
		}
		next.Status = store.AgentJobRunning
		next.Attempt++
		claimed = next
		return nil
	})
	return claimed, err
}

func (w *WorkerManager) runJob(parent context.Context, job *store.AgentJob) error {
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	w.mu.Lock()
	w.cancel[job.ID] = cancel
	w.mu.Unlock()

	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case at := <-ticker.C:
				_ = w.DB.AgentJobs().Heartbeat(context.Background(), job.ID, at)
			}
		}
	}()

	var controller agent.StepController
	var contextBlocks []string
	if w.Tasks != nil && job.WorkTaskID != "" {
		if _, err := w.Tasks.SetActive(ctx, job.OwnerUserID, job.WorkTaskID, job.ID, job.SessionID); err != nil {
			_ = w.DB.AgentJobs().AddEvent(context.Background(), job.ID, "cancelled",
				"任务已停止，未开始执行："+err.Error(), w.now())
			close(heartbeatDone)
			cancel()
			w.mu.Lock()
			delete(w.cancel, job.ID)
			w.mu.Unlock()
			return nil
		}
		controller = NewWorkerStepController(w.DB, w.Tasks, job)
		if w.Context != nil {
			blocks, receipts, contextErr := w.Context(ctx, job)
			if contextErr != nil {
				log.Printf("[superagent-worker] context %s: %v", job.ID, contextErr)
			} else {
				contextBlocks = blocks
				if len(receipts) > 0 {
					_ = w.Tasks.RecordContextReceipt(ctx, job.OwnerUserID, job.WorkTaskID, job.ID, receipts)
				}
			}
		}
	}

	result, runErr := w.Runner.RunWorker(ctx, WorkerRequest{
		OwnerID: job.OwnerUserID, JobID: job.ID, TaskID: job.WorkTaskID, ThreadID: job.ThreadID,
		SessionID: job.SessionID, Goal: job.Task, Profile: job.Profile,
		StepController: controller, ContextBlocks: contextBlocks,
		OnEvent: func(ev WorkerEvent) {
			if ev.Kind != "tool" && ev.Kind != "tool_done" && ev.Kind != "clarify" {
				return
			}
			body := strings.TrimSpace(ev.Detail)
			if body == "" {
				body = ev.Name
			}
			if body == "" {
				return
			}
			_ = w.DB.AgentJobs().AddEvent(context.Background(), job.ID, "progress", truncateRunes(body, 1000), time.Now())
		},
	})
	// cancel() 之前取快照，避免把失败/超时误判为取消。
	ctxErr := ctx.Err()
	close(heartbeatDone)
	cancel()
	w.mu.Lock()
	delete(w.cancel, job.ID)
	w.mu.Unlock()

	status := store.AgentJobCompleted
	errorText := ""
	switch {
	case runErr != nil && errors.Is(ctxErr, context.DeadlineExceeded):
		status, errorText = store.AgentJobTimeout, runErr.Error()
	case runErr != nil && errors.Is(ctxErr, context.Canceled):
		status, errorText = store.AgentJobCancelled, runErr.Error()
	case runErr != nil:
		status, errorText = store.AgentJobFailed, runErr.Error()
	}

	output, summary, sessionID := "", "", job.SessionID
	if result != nil {
		output = truncateRunes(result.Reply, 20000)
		summary = truncateRunes(result.Summary, 4000)
		if summary == "" {
			summary = truncateRunes(result.Reply, 4000)
		}
		if result.SessionID != "" {
			sessionID = result.SessionID
		}
	}

	now := w.now()
	body := workerNotification(status, summary, errorText)
	var completedEventID int64
	err := w.DB.WithTx(context.Background(), func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		changed, err := repos.AgentJobs.FinishWorker(context.Background(), job.ID, status,
			output, summary, sessionID, errorText, now)
		if err != nil || !changed {
			return err
		}
		if err := repos.AgentJobs.AddEvent(context.Background(), job.ID, status,
			firstNonEmpty(errorText, summary, output), now); err != nil {
			return err
		}
		event, err := appendEventRecord(context.Background(), repos, job.OwnerUserID, "worker."+status,
			"worker_task", job.ID, map[string]any{
				"thread_id": job.ThreadID, "trigger_id": job.ParentTriggerID, "status": status,
				"session_id": sessionID, "summary": truncateRunes(summary, 1000),
				"error": truncateRunes(errorText, 1000),
			})
		if err != nil {
			return err
		}
		completedEventID = event.ID
		return repos.AgentNotifications.Add(context.Background(), job.OwnerUserID, job.ID, body, now)
	})
	if err != nil {
		return fmt.Errorf("finish worker %s: %w", job.ID, err)
	}
	w.syncTaskSegment(job, result, status, sessionID, summary, errorText)
	if status == store.AgentJobCompleted {
		if job.ThreadID != "" && summary != "" {
			if changed, err := w.DB.Threads().UpdateSummary(context.Background(), job.OwnerUserID,
				job.ThreadID, completedEventID, summary, now); err != nil {
				log.Printf("[superagent-worker] update thread summary %s: %v", job.ThreadID, err)
			} else if !changed {
				log.Printf("[superagent-worker] thread summary %s not advanced (stale cursor)", job.ThreadID)
			}
		}
		if w.OnResult != nil && summary != "" {
			// 结论必须先同步进入统一记忆再结束 Worker，避免主会话立刻追问时召回不到。
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := w.OnResult(ctx, job, summary)
			cancel()
			if err != nil {
				log.Printf("[superagent-worker] persist result %s: %v", job.ID, err)
			}
		}
	}
	w.signal()
	return nil
}

func (w *WorkerManager) syncTaskSegment(job *store.AgentJob, result *WorkerResult, status,
	sessionID, summary, errorText string) {
	if w.Tasks == nil || job == nil || job.WorkTaskID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	detail, err := w.Tasks.Get(ctx, job.OwnerUserID, job.WorkTaskID)
	if err != nil || detail == nil || detail.Task == nil {
		return
	}
	task := detail.Task
	if task.Status == store.WorkTaskCancelled || task.Status == store.WorkTaskCompleted {
		return
	}
	if result != nil && (result.Paused || result.Cancelled) {
		return
	}
	switch status {
	case store.AgentJobFailed, store.AgentJobTimeout, store.AgentJobInterrupted:
		_, _ = w.Tasks.Fail(ctx, job.OwnerUserID, job.WorkTaskID, job.ID,
			firstNonEmpty(errorText, "Worker 执行失败"))
		return
	}
	if result != nil && result.WaitingKind != "" {
		_, _ = w.Tasks.Wait(ctx, job.OwnerUserID, job.WorkTaskID, job.ID, sessionID,
			task.Version, result.WaitingKind, firstNonEmpty(summary, "等待用户输入"),
			firstNonEmpty(result.WaitingAction(), "继续任务"), result.WaitingPayload)
		return
	}
	if status == store.AgentJobCompleted && task.Status == store.WorkTaskActive {
		submitted := 0
		if w.Artifacts != nil {
			if artifacts, listErr := store.NewRepos(w.DB).Artifacts.ListByTask(ctx, job.OwnerUserID, job.WorkTaskID); listErr == nil {
				for _, artifact := range artifacts {
					if artifact.Status != store.ArtifactDraft {
						continue
					}
					if _, submitErr := w.Artifacts.SubmitForAcceptance(ctx, job.OwnerUserID,
						artifact.ID, artifact.CurrentVersion); submitErr == nil {
						submitted++
					}
				}
			}
		}
		if submitted == 0 {
			_, _ = w.Tasks.PendingAcceptance(ctx, job.OwnerUserID, job.WorkTaskID, job.ID,
				task.Version, firstNonEmpty(summary, "执行段完成"))
		}
	}
}

// WaitingAction 给恢复检查点一个可读的下一步。
func (r *WorkerResult) WaitingAction() string {
	if r == nil || len(r.WaitingPayload) == 0 {
		return ""
	}
	return truncateRunes(string(r.WaitingPayload), 500)
}

// CreateWorker 幂等创建 Worker 任务并订阅 Thread。
func (w *WorkerManager) CreateWorker(ctx context.Context, ownerID string, trigger *store.Trigger, decision Decision) (string, error) {
	if w.DB == nil || w.Runner == nil {
		return "", errors.New("WorkerManager 未启用")
	}
	if trigger == nil {
		return "", errors.New("缺少来源 Trigger")
	}
	goal := strings.TrimSpace(decision.Goal)
	if goal == "" {
		return "", errors.New("Worker 目标为空")
	}
	now := w.now()
	if w.Tasks != nil {
		task, err := w.Tasks.Create(ctx, worktask.CreateInput{
			OwnerID: ownerID, Title: firstNonEmpty(trigger.Title, decision.Goal), Goal: goal,
			Priority: decision.Priority, Profile: decision.Profile, SourceTriggerID: trigger.ID,
			ThreadID: trigger.ThreadID, IdempotencyKey: "superagent:" + trigger.ID,
			Start: true, CreatedBy: "router", JobThreadID: trigger.ThreadID,
			JobParentTriggerID: trigger.ID,
		})
		if err != nil {
			return "", err
		}
		job, err := w.DB.AgentJobs().GetByIdempotencyKey(ctx, ownerID, "task:"+task.ID+":primary")
		if err != nil {
			return "", err
		}
		if job.ThreadID != "" {
			if err := w.DB.ThreadSubscriptions().Add(ctx, ownerID, job.ThreadID, job.ID, now); err != nil {
				return "", err
			}
		}
		_ = appendEvent(ctx, store.NewRepos(w.DB), ownerID, "worker.created", "worker_task", job.ID, map[string]any{
			"trigger_id": trigger.ID, "thread_id": trigger.ThreadID, "profile": decision.Profile,
			"work_task_id": task.ID,
		})
		w.signal()
		return job.ID, nil
	}
	idempotencyKey := "superagent:" + trigger.ID
	job := &store.AgentJob{
		ID: uuid.NewString(), OwnerUserID: ownerID, AgentID: WorkerAgentID,
		Task: truncateRunes(goal, 12000), Status: store.AgentJobQueued,
		IdempotencyKey: idempotencyKey, ThreadID: trigger.ThreadID, ParentTriggerID: trigger.ID,
		Profile: decision.Profile, Priority: decision.Priority, CreatedAt: now, UpdatedAt: now,
	}
	err := w.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		if err := repos.AgentJobs.Create(ctx, job); err != nil {
			if existing, getErr := repos.AgentJobs.GetByIdempotencyKey(ctx, ownerID, idempotencyKey); getErr == nil {
				job = existing
				return nil
			}
			return err
		}
		if job.ThreadID != "" {
			if err := repos.ThreadSubscriptions.Add(ctx, ownerID, job.ThreadID, job.ID, now); err != nil {
				return err
			}
		}
		if err := repos.AgentJobs.AddEvent(ctx, job.ID, "queued", "Worker 已入队", now); err != nil {
			return err
		}
		return appendEvent(ctx, repos, ownerID, "worker.created", "worker_task", job.ID, map[string]any{
			"trigger_id": trigger.ID, "thread_id": trigger.ThreadID, "profile": decision.Profile,
		})
	})
	if err != nil {
		return "", err
	}
	w.signal()
	return job.ID, nil
}

// RouteToWorker 把 Thread 的后续事件直通给已订阅 Worker。
func (w *WorkerManager) RouteToWorker(ctx context.Context, ownerID string, trigger *store.Trigger, decision Decision) error {
	if w.DB == nil {
		return errors.New("WorkerManager 未启用")
	}
	job, err := w.DB.AgentJobs().Get(ctx, ownerID, decision.WorkerTaskID)
	if err != nil {
		return fmt.Errorf("Worker 任务不存在")
	}
	now := w.now()
	body := truncateRunes("Thread 新事件："+firstNonEmpty(trigger.Title, trigger.Body), 2000)
	if w.Tasks != nil && job.WorkTaskID != "" {
		if _, err := w.Tasks.AddThreadEvent(ctx, ownerID, job.WorkTaskID, trigger.ID, body,
			"trigger:"+trigger.ID); err != nil {
			return err
		}
		return w.DB.WithTx(ctx, func(tx *sql.Tx) error {
			repos := store.NewRepos(tx)
			if trigger.ThreadID != "" {
				if err := repos.ThreadSubscriptions.Add(ctx, ownerID, trigger.ThreadID, job.ID, now); err != nil {
					return err
				}
			}
			return appendEvent(ctx, repos, ownerID, "worker.routed", "worker_task", job.ID, map[string]any{
				"trigger_id": trigger.ID, "thread_id": trigger.ThreadID,
				"work_task_id": job.WorkTaskID,
			})
		})
	}
	if job.Status == store.AgentJobCompleted || job.Status == store.AgentJobFailed ||
		job.Status == store.AgentJobCancelled || job.Status == store.AgentJobTimeout ||
		job.Status == store.AgentJobInterrupted {
		return errors.New("Worker 任务已结束，不能再直通事件")
	}
	return w.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		if trigger.ThreadID != "" {
			if err := repos.ThreadSubscriptions.Add(ctx, ownerID, trigger.ThreadID, job.ID, now); err != nil {
				return err
			}
		}
		if err := repos.AgentJobs.AddEvent(ctx, job.ID, "thread_event", body, now); err != nil {
			return err
		}
		return appendEvent(ctx, repos, ownerID, "worker.routed", "worker_task", job.ID, map[string]any{
			"trigger_id": trigger.ID, "thread_id": trigger.ThreadID,
		})
	})
}

func (w *WorkerManager) Cancel(ctx context.Context, ownerID, id string) (*store.AgentJob, error) {
	job, err := w.DB.AgentJobs().Get(ctx, ownerID, id)
	if err != nil {
		return nil, errors.New("任务不存在")
	}
	now := w.now()
	cancelled := false
	err = w.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		changed, err := repos.AgentJobs.Cancel(ctx, ownerID, id, now)
		if err != nil || !changed {
			return err
		}
		cancelled = true
		body := "Worker 任务已取消。任务：" + id
		if err := repos.AgentJobs.AddEvent(ctx, id, "cancelled", body, now); err != nil {
			return err
		}
		if err := appendEvent(ctx, repos, ownerID, "worker.cancelled", "worker_task", id, map[string]any{
			"thread_id": job.ThreadID, "trigger_id": job.ParentTriggerID, "status": store.AgentJobCancelled,
		}); err != nil {
			return err
		}
		return repos.AgentNotifications.Add(ctx, ownerID, id, body, now)
	})
	if err != nil {
		return nil, err
	}
	if !cancelled {
		return nil, errors.New("任务已结束或不存在，不能取消")
	}
	w.mu.Lock()
	if cancel := w.cancel[id]; cancel != nil {
		cancel()
	}
	w.mu.Unlock()
	w.signal()
	return w.DB.AgentJobs().Get(ctx, ownerID, id)
}

func workerNotification(status, summary, errorText string) string {
	label := map[string]string{
		store.AgentJobCompleted: "已完成", store.AgentJobFailed: "失败",
		store.AgentJobTimeout: "超时", store.AgentJobCancelled: "已取消",
	}[status]
	if label == "" {
		label = status
	}
	text := strings.TrimSpace(summary)
	if text == "" {
		text = strings.TrimSpace(errorText)
	}
	if text == "" {
		return "Worker 任务" + label
	}
	return "Worker 任务" + label + "：" + truncateRunes(text, 500)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
