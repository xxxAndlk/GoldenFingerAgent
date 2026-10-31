package agenthub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

// DelegateFunc 是后台任务实际执行通道，默认使用 Manager.DelegateAt。
type DelegateFunc func(context.Context, string, string, string) (Invocation, error)

// Dispatcher 提供持久化、至少一次通知的异步 Agent 任务队列。
// 当前为单 Worker，避免桌面 Agent 争用焦点；后续可在不改变存储契约的情况下接入外部队列。
type Dispatcher struct {
	DB       *store.DB
	Manager  *Manager
	Delegate DelegateFunc
	Timeout  time.Duration

	wake   chan struct{}
	mu     sync.Mutex
	cancel map[string]context.CancelFunc
	root   context.Context
}

func NewDispatcher(db *store.DB, manager *Manager) *Dispatcher {
	return &Dispatcher{DB: db, Manager: manager, Timeout: 30 * time.Minute, wake: make(chan struct{}, 1), cancel: make(map[string]context.CancelFunc)}
}

// Start 恢复持久化状态并启动 Worker。running 不盲目重跑，标记 interrupted 等待核对。
func (d *Dispatcher) Start(ctx context.Context) error {
	d.root = ctx
	now := time.Now()
	interrupted, err := d.DB.AgentJobs().InterruptRunning(ctx, now)
	if err != nil {
		return err
	}
	for _, job := range interrupted {
		if err := d.DB.WithTx(ctx, func(tx *sql.Tx) error {
			repos := store.NewRepos(tx)
			body := "Agent 任务在程序重启前尚未确认结果，已保留记录，请核对后再决定是否重试。任务：" + job.ID
			if err := repos.AgentJobs.AddEvent(ctx, job.ID, "interrupted", body, now); err != nil {
				return err
			}
			return repos.AgentNotifications.Add(ctx, job.OwnerUserID, job.ID, body, now)
		}); err != nil {
			return err
		}
	}
	go d.loop()
	return nil
}

func (d *Dispatcher) Submit(ctx context.Context, ownerID, agentID, task, workDir, idempotencyKey string) (*store.AgentJob, error) {
	if d.DB == nil {
		return nil, errors.New("异步任务存储未配置")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, errors.New("任务不能为空")
	}
	if len(task) > 12000 {
		return nil, errors.New("任务不能超过 12000 字符")
	}
	resolvedWorkDir, err := d.Manager.resolveWorkDir(workDir)
	if err != nil {
		return nil, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	}
	if len(idempotencyKey) > 200 {
		return nil, errors.New("幂等键不能超过 200 字符")
	}
	now := time.Now()
	job := &store.AgentJob{ID: uuid.NewString(), OwnerUserID: ownerID, AgentID: agentID, Task: task,
		WorkDir: resolvedWorkDir, Status: store.AgentJobQueued, IdempotencyKey: idempotencyKey, CreatedAt: now, UpdatedAt: now}
	err = d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		if err := repos.AgentJobs.Create(ctx, job); err != nil {
			return err
		}
		return repos.AgentJobs.AddEvent(ctx, job.ID, "queued", "任务已持久化，等待后台执行", now)
	})
	if err != nil {
		if existing, getErr := d.DB.AgentJobs().GetByIdempotencyKey(ctx, ownerID, idempotencyKey); getErr == nil {
			return existing, nil
		}
		return nil, err
	}
	d.signal()
	return job, nil
}

func (d *Dispatcher) Get(ctx context.Context, ownerID, id string) (*store.AgentJob, []store.AgentJobEvent, error) {
	job, err := d.DB.AgentJobs().Get(ctx, ownerID, id)
	if err != nil {
		return nil, nil, err
	}
	events, err := d.DB.AgentJobs().Events(ctx, ownerID, id)
	return job, events, err
}

func (d *Dispatcher) List(ctx context.Context, ownerID string, limit int) ([]store.AgentJob, error) {
	return d.DB.AgentJobs().List(ctx, ownerID, limit)
}

func (d *Dispatcher) Cancel(ctx context.Context, ownerID, id string) (*store.AgentJob, error) {
	now := time.Now()
	cancelled := false
	err := d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		changed, err := repos.AgentJobs.Cancel(ctx, ownerID, id, now)
		if err != nil || !changed {
			return err
		}
		cancelled = true
		body := "Agent 任务已取消。任务：" + id
		if err := repos.AgentJobs.AddEvent(ctx, id, "cancelled", body, now); err != nil {
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
	d.mu.Lock()
	if cancel := d.cancel[id]; cancel != nil {
		cancel()
	}
	d.mu.Unlock()
	return d.DB.AgentJobs().Get(ctx, ownerID, id)
}

func (d *Dispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) loop() {
	for {
		if err := d.runNext(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[agent-job] worker: %v", err)
		}
		select {
		case <-d.root.Done():
			return
		case <-d.wake:
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *Dispatcher) claim(ctx context.Context) (*store.AgentJob, error) {
	var job *store.AgentJob
	err := d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		next, err := repos.AgentJobs.FirstQueued(ctx)
		if err != nil || next == nil {
			return err
		}
		running, err := repos.AgentJobs.MarkRunning(ctx, next.ID, time.Now())
		if err != nil || !running {
			return err
		}
		if err := repos.AgentJobs.AddEvent(ctx, next.ID, "running", fmt.Sprintf("后台开始执行，第 %d 次尝试", next.Attempt+1), time.Now()); err != nil {
			return err
		}
		job = next
		job.Status = store.AgentJobRunning
		return nil
	})
	return job, err
}

func (d *Dispatcher) runNext() error {
	job, err := d.claim(d.root)
	if err != nil || job == nil {
		return err
	}
	delegate := d.Delegate
	if delegate == nil {
		delegate = d.Manager.DelegateAt
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(d.root, timeout)
	d.mu.Lock()
	d.cancel[job.ID] = cancel
	d.mu.Unlock()
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case at := <-ticker.C:
				_ = d.DB.AgentJobs().Heartbeat(context.Background(), job.ID, at)
			}
		}
	}()
	invocation, runErr := delegate(ctx, job.AgentID, job.Task, job.WorkDir)
	// 必须在 cancel() 之前取快照：否则 ctx.Err() 恒为 Canceled，
	// 会让所有失败/超时任务都被误标成“已取消”。
	ctxErr := ctx.Err()
	close(heartbeatDone)
	cancel()
	d.mu.Lock()
	delete(d.cancel, job.ID)
	d.mu.Unlock()

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
	if errorText == "" && invocation.State == "error" {
		status, errorText = store.AgentJobFailed, invocation.Output
	}
	now := time.Now()
	changed, err := d.DB.AgentJobs().Finish(context.Background(), job.ID, status, invocation.Output, errorText, invocation.TaskID, now)
	if err != nil || !changed {
		return err
	}
	body := agentJobNotification(job.AgentID, job.ID, status, invocation.Output, errorText)
	return d.DB.WithTx(context.Background(), func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		if err := repos.AgentJobs.AddEvent(context.Background(), job.ID, status, firstNonEmpty(errorText, invocation.Output), now); err != nil {
			return err
		}
		return repos.AgentNotifications.Add(context.Background(), job.OwnerUserID, job.ID, body, now)
	})
}

func agentJobNotification(agentID, jobID, status, output, errorText string) string {
	label := map[string]string{store.AgentJobCompleted: "已完成", store.AgentJobFailed: "失败", store.AgentJobTimeout: "超时", store.AgentJobCancelled: "已取消"}[status]
	if label == "" {
		label = status
	}
	body := "Agent 任务" + label + "（" + agentID + "，任务 " + jobID + "）"
	detail := firstNonEmpty(errorText, output)
	if detail != "" {
		body += "\n" + truncateText(detail, 1500)
	}
	return body
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
