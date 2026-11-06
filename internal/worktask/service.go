// Package worktask 实现跨消息、跨执行段的长期工作控制面。
// 它只依赖 store；Worker 执行器和 Router 通过回调接入，避免包循环。
package worktask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

var (
	ErrInvalid  = errors.New("worktask: invalid input")
	ErrConflict = store.ErrConflict
)

type Service struct {
	DB           *store.DB
	Now          func() time.Time
	ArtifactRoot string
	// OnEnqueue 在新执行段入库后唤醒 WorkerManager；不负责执行。
	OnEnqueue func(ctx context.Context, ownerID, taskID, jobID string)
	// OnCancel 让 WorkerManager 立即取消正在运行的 context。
	OnCancel func(ctx context.Context, ownerID, taskID, jobID string)
	// DeviceRecoveryCheck 在 waiting_device 恢复前重新校验绑定设备与能力。
	DeviceRecoveryCheck func(ctx context.Context, ownerID string, task *store.WorkTask) (bool, error)
}

func New(db *store.DB) *Service {
	return &Service{DB: db, Now: time.Now}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type CreateInput struct {
	OwnerID            string
	Title              string
	Goal               string
	CompletionCriteria string
	Priority           int
	Profile            string
	SourceTriggerID    string
	ThreadID           string
	IdempotencyKey     string
	Status             string
	Start              bool
	CreatedBy          string
	JobThreadID        string
	JobParentTriggerID string
}

type MessageResult struct {
	Task      *store.WorkTask      `json:"task"`
	Inbox     *store.WorkTaskInbox `json:"inbox"`
	Job       *store.AgentJob      `json:"job,omitempty"`
	Created   bool                 `json:"created"`
	Delivered string               `json:"delivery"`
}

type Detail struct {
	Task           *store.WorkTask                  `json:"task"`
	Live           *store.WorkTaskLiveState         `json:"live_state"`
	Threads        []store.WorkTaskThread           `json:"threads"`
	Inbox          []store.WorkTaskInbox            `json:"inbox"`
	Jobs           []store.AgentJob                 `json:"jobs"`
	Artifacts      []store.Artifact                 `json:"artifacts"`
	ArtifactLatest map[string]store.ArtifactVersion `json:"artifact_latest,omitempty"`
	Checkpoints    []store.WorkTaskCheckpoint       `json:"checkpoints"`
	Grants         []store.ActionGrant              `json:"grants"`
	Invocations    []store.ToolInvocation           `json:"tool_invocations"`
}

func normalizeProfile(profile string) (string, error) {
	switch strings.TrimSpace(profile) {
	case "", store.WorkTaskProfileRead:
		return store.WorkTaskProfileRead, nil
	case store.WorkTaskProfileInternal:
		return store.WorkTaskProfileInternal, nil
	case store.WorkTaskProfileExternal:
		return store.WorkTaskProfileExternal, nil
	default:
		return "", fmt.Errorf("%w: profile 必须是 read/internal/external", ErrInvalid)
	}
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*store.WorkTask, error) {
	ownerID := strings.TrimSpace(in.OwnerID)
	title := strings.TrimSpace(in.Title)
	goal := strings.TrimSpace(in.Goal)
	if ownerID == "" || title == "" || goal == "" {
		return nil, fmt.Errorf("%w: owner/title/goal required", ErrInvalid)
	}
	profile, err := normalizeProfile(in.Profile)
	if err != nil {
		return nil, err
	}
	if in.Priority < 0 || in.Priority > 9 {
		return nil, fmt.Errorf("%w: priority 必须在 0..9", ErrInvalid)
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = store.WorkTaskDraft
	}
	if status != store.WorkTaskDraft && status != store.WorkTaskQueued {
		return nil, fmt.Errorf("%w: 新任务只能从 draft 或 queued 开始", ErrInvalid)
	}
	if in.Start {
		status = store.WorkTaskQueued
	}
	idempotencyKey := strings.TrimSpace(in.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = "task:" + uuid.NewString()
	}
	now := s.now()
	task := &store.WorkTask{
		OwnerUserID: ownerID, Title: title, Goal: goal,
		CompletionCriteria: strings.TrimSpace(in.CompletionCriteria),
		Status:             status, Priority: in.Priority, Profile: profile,
		SourceTriggerID: strings.TrimSpace(in.SourceTriggerID),
		IdempotencyKey:  idempotencyKey, CreatedAt: now, UpdatedAt: now,
	}
	var job *store.AgentJob
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		if err := repos.WorkTasks.Create(ctx, task); err != nil {
			if !errors.Is(err, store.ErrConflict) {
				return err
			}
			existing, getErr := repos.WorkTasks.GetByIdempotencyKey(ctx, ownerID, idempotencyKey)
			if getErr != nil {
				return getErr
			}
			task = existing
			return nil
		}
		if threadID := strings.TrimSpace(in.ThreadID); threadID != "" {
			if err := repos.WorkTaskThreads.Add(ctx, &store.WorkTaskThread{
				OwnerUserID: ownerID, TaskID: task.ID, ThreadID: threadID,
				Relation: store.WorkTaskRelationOrigin, Confidence: 1,
				Reason: "创建任务时指定", CreatedBy: firstNonEmpty(in.CreatedBy, "system"),
			}); err != nil {
				return err
			}
		}
		if in.Start {
			job, err = createJobTx(ctx, repos, task, task.Goal, "task:"+task.ID+":primary",
				strings.TrimSpace(in.JobThreadID), strings.TrimSpace(in.JobParentTriggerID), "", now)
			if err != nil {
				return err
			}
		}
		return appendTaskEvent(ctx, repos, ownerID, "task.created", task, map[string]any{
			"status": task.Status, "profile": task.Profile, "job_id": jobID(job),
		})
	})
	if err != nil {
		return nil, err
	}
	if job != nil {
		s.enqueue(ctx, ownerID, task.ID, job.ID)
	}
	return task, nil
}

func (s *Service) Get(ctx context.Context, ownerID, id string) (*Detail, error) {
	repos := store.NewRepos(s.DB)
	task, err := repos.WorkTasks.Get(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	live, err := repos.WorkTaskLive.Get(ctx, ownerID, id)
	if errors.Is(err, store.ErrNotFound) {
		live = &store.WorkTaskLiveState{TaskID: id, OwnerUserID: ownerID, WaitingKind: store.WaitingNone}
	} else if err != nil {
		return nil, err
	}
	threads, err := repos.WorkTaskThreads.ListByTask(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	inbox, err := repos.WorkTaskInbox.ListByTask(ctx, ownerID, id, 50)
	if err != nil {
		return nil, err
	}
	jobs, err := repos.AgentJobs.ListByTask(ctx, ownerID, id, 50)
	if err != nil {
		return nil, err
	}
	artifacts, err := repos.Artifacts.ListByTask(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]store.ArtifactVersion, len(artifacts))
	for _, artifact := range artifacts {
		if versions, listErr := repos.ArtifactVersions.List(ctx, ownerID, artifact.ID); listErr == nil && len(versions) > 0 {
			latest[artifact.ID] = versions[0]
		}
	}
	checkpoints, err := repos.Checkpoints.ListForTask(ctx, ownerID, id, 50)
	if err != nil {
		return nil, err
	}
	grants, err := repos.Grants.ListByTask(ctx, ownerID, id, 50)
	if err != nil {
		return nil, err
	}
	invocations, err := repos.ToolInvocations.ListByTask(ctx, ownerID, id, 50)
	if err != nil {
		return nil, err
	}
	return &Detail{
		Task: task, Live: live, Threads: threads, Inbox: inbox, Jobs: jobs,
		Artifacts: artifacts, ArtifactLatest: latest, Checkpoints: checkpoints, Grants: grants,
		Invocations: invocations,
	}, nil
}

func (s *Service) List(ctx context.Context, ownerID, status string, limit int) ([]store.WorkTask, error) {
	return store.NewRepos(s.DB).WorkTasks.List(ctx, ownerID, status, limit)
}

func (s *Service) AddMessage(ctx context.Context, ownerID, taskID, text string, expectedVersion *int, idempotencyKey string) (*MessageResult, error) {
	return s.addInboxMessage(ctx, ownerID, taskID, text, expectedVersion, idempotencyKey,
		store.InboxSourceUser, "", store.InboxKindSteer)
}

// AddThreadEvent 把关联 Thread 的新事件写入 Task Inbox，来源保留为 thread。
func (s *Service) AddThreadEvent(ctx context.Context, ownerID, taskID, triggerID, text, idempotencyKey string) (*MessageResult, error) {
	return s.addInboxMessage(ctx, ownerID, taskID, text, nil, idempotencyKey,
		store.InboxSourceThread, triggerID, store.InboxKindSteer)
}

func (s *Service) addInboxMessage(ctx context.Context, ownerID, taskID, text string, expectedVersion *int,
	idempotencyKey, sourceKind, sourceEventID, inboxKind string) (*MessageResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%w: 消息不能为空", ErrInvalid)
	}
	if len([]rune(text)) > 4000 {
		return nil, fmt.Errorf("%w: 消息不能超过 4000 字符", ErrInvalid)
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		idempotencyKey = "message:" + taskID + ":" + uuid.NewString()
	}
	now := s.now()
	var result *MessageResult
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion != nil && *expectedVersion != task.Version {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, *expectedVersion)
		}
		// 终态任务不能直接追加指令：先重新打开，再继续。
		if task.Status == store.WorkTaskCancelled || task.Status == store.WorkTaskCompleted {
			return fmt.Errorf("%w: 已结束的任务不能追加消息，请先重新打开", ErrConflict)
		}
		if sourceKind == "" {
			sourceKind = store.InboxSourceUser
		}
		if inboxKind == "" {
			inboxKind = store.InboxKindSteer
		}
		inbox := &store.WorkTaskInbox{
			OwnerUserID: ownerID, TaskID: taskID, SourceKind: sourceKind,
			SourceEventID: sourceEventID, Kind: inboxKind, Body: text, Priority: 10,
			IdempotencyKey: idempotencyKey,
		}
		created, item, err := repos.WorkTaskInbox.Add(ctx, inbox)
		if err != nil {
			return err
		}
		result = &MessageResult{Task: task, Inbox: item, Created: created, Delivered: "queued"}
		if !created {
			return nil
		}
		startJob := false
		switch {
		case store.IsWaitingStatus(task.Status):
			if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskActive) {
				return fmt.Errorf("%w: 状态 %s 不能恢复", ErrConflict, task.Status)
			}
			changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskActive, now)
			if err != nil || !changed {
				return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
			}
			task.Status = store.WorkTaskActive
			task.Version++
			task.UpdatedAt = now
			startJob = true
		case task.Status == store.WorkTaskActive || task.Status == store.WorkTaskQueued:
			startJob = true
		case task.Status == store.WorkTaskPaused:
			result.Delivered = "queued_for_resume"
		case task.Status == store.WorkTaskPendingAccept:
			return fmt.Errorf("%w: 待验收任务请先验收或要求修改", ErrConflict)
		case task.Status == store.WorkTaskCompleted:
			return fmt.Errorf("%w: 已完成任务请先重新打开", ErrConflict)
		default:
			return fmt.Errorf("%w: 状态 %s 不能接收消息", ErrConflict, task.Status)
		}
		if startJob {
			active, err := repos.AgentJobs.ActivePrimaryForTask(ctx, ownerID, taskID)
			if err != nil {
				return err
			}
			if active == nil {
				job, err := createJobTx(ctx, repos, task, "任务补充："+text,
					"task:"+taskID+":segment:"+item.ID, "", "", task.CurrentSessionID, now)
				if err != nil {
					return err
				}
				result.Job = job
				result.Delivered = "new_segment"
			}
		}
		result.Task = task
		return appendTaskEvent(ctx, repos, ownerID, "task.inbox.queued", task, map[string]any{
			"inbox_id": item.ID, "kind": item.Kind, "job_id": jobID(result.Job),
		})
	})
	if err != nil {
		return nil, err
	}
	if result.Job != nil {
		s.enqueue(ctx, ownerID, taskID, result.Job.ID)
	}
	return result, nil
}

func (s *Service) Pause(ctx context.Context, ownerID, taskID string, expectedVersion int) (*store.WorkTask, error) {
	now := s.now()
	var updated *store.WorkTask
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskPaused) {
			return fmt.Errorf("%w: 当前状态 %s 不能暂停", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskPaused, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskPaused, task.Version+1, now
		if _, err := updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`),
			"paused", "用户暂停任务", ""); err != nil {
			return err
		}
		_, _, err = repos.WorkTaskInbox.Add(ctx, &store.WorkTaskInbox{
			OwnerUserID: ownerID, TaskID: taskID, SourceKind: store.InboxSourceControl,
			Kind: store.InboxKindPause, Priority: 100,
			IdempotencyKey: "pause:" + taskID + ":" + fmt.Sprint(task.Version),
		})
		if err != nil {
			return err
		}
		_, _ = repos.WorkTaskInbox.CancelQueuedByKind(ctx, ownerID, taskID, store.InboxKindResume)
		_, _ = repos.WorkTaskInbox.CancelQueuedByKind(ctx, ownerID, taskID, store.InboxKindRecover)
		if active, _ := repos.AgentJobs.ActivePrimaryForTask(ctx, ownerID, taskID); active != nil && active.Status == store.AgentJobQueued {
			_, _ = repos.AgentJobs.Cancel(ctx, ownerID, active.ID, now)
		}
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.paused", task, map[string]any{
			"status": task.Status, "version": task.Version,
		})
	})
	return updated, err
}

func (s *Service) Resume(ctx context.Context, ownerID, taskID string, expectedVersion int) (*MessageResult, error) {
	now := s.now()
	var result *MessageResult
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskQueued) {
			return fmt.Errorf("%w: 当前状态 %s 不能恢复", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskQueued, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskQueued, task.Version+1, now
		_, _ = repos.WorkTaskInbox.CancelQueuedByKind(ctx, ownerID, taskID, store.InboxKindPause)
		_, _ = repos.WorkTaskInbox.CancelQueuedByKind(ctx, ownerID, taskID, store.InboxKindCancel)
		if _, err := updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`),
			"resumed", "用户恢复任务", "继续执行"); err != nil {
			return err
		}
		inbox := &store.WorkTaskInbox{
			OwnerUserID: ownerID, TaskID: taskID, SourceKind: store.InboxSourceControl,
			Kind: store.InboxKindResume, Priority: 90,
			IdempotencyKey: "resume:" + taskID + ":" + fmt.Sprint(task.Version),
		}
		created, item, err := repos.WorkTaskInbox.Add(ctx, inbox)
		if err != nil {
			return err
		}
		if !created {
			item, _ = repos.WorkTaskInbox.GetByIdempotencyKey(ctx, ownerID, inbox.IdempotencyKey)
		}
		job, err := createJobTx(ctx, repos, task, "继续执行任务："+task.Goal,
			"task:"+taskID+":resume:"+fmt.Sprint(task.Version), "", "", task.CurrentSessionID, now)
		if err != nil {
			return err
		}
		result = &MessageResult{Task: task, Inbox: item, Job: job, Created: created, Delivered: "new_segment"}
		return appendTaskEvent(ctx, repos, ownerID, "task.resumed", task, map[string]any{
			"status": task.Status, "job_id": job.ID,
		})
	})
	if err == nil && result != nil && result.Job != nil {
		s.enqueue(ctx, ownerID, taskID, result.Job.ID)
	}
	return result, err
}

func (s *Service) Cancel(ctx context.Context, ownerID, taskID string, expectedVersion int) (*store.WorkTask, error) {
	now := s.now()
	var updated *store.WorkTask
	var runningJobID string
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskCancelled) {
			return fmt.Errorf("%w: 当前状态 %s 不能取消", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskCancelled, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskCancelled, task.Version+1, now
		if active, _ := repos.AgentJobs.ActivePrimaryForTask(ctx, ownerID, taskID); active != nil {
			if active.Status == store.AgentJobRunning {
				runningJobID = active.ID
			}
			_, _ = repos.AgentJobs.Cancel(ctx, ownerID, active.ID, now)
		}
		_, _ = repos.WorkTaskInbox.CancelQueued(ctx, ownerID, taskID)
		_, _ = repos.Grants.RevokeForTask(ctx, ownerID, taskID, now)
		_, _ = updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`),
			"cancelled", "任务已取消", "")
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.cancelled", task, map[string]any{
			"status": task.Status, "job_id": runningJobID,
		})
	})
	if err == nil && runningJobID != "" && s.OnCancel != nil {
		s.OnCancel(ctx, ownerID, taskID, runningJobID)
	}
	return updated, err
}

func (s *Service) Recover(ctx context.Context, ownerID, taskID string, expectedVersion int) (*MessageResult, error) {
	now := s.now()
	var result *MessageResult
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskQueued) {
			return fmt.Errorf("%w: 当前状态 %s 不能恢复", ErrConflict, task.Status)
		}
		if task.Status == store.WorkTaskWaitingDevice && s.DeviceRecoveryCheck != nil {
			ok, checkErr := s.DeviceRecoveryCheck(ctx, ownerID, task)
			if checkErr != nil {
				return checkErr
			}
			if !ok {
				return fmt.Errorf("%w: 绑定设备已撤销或离线，不能恢复", ErrConflict)
			}
		}
		checkpoint, _ := repos.Checkpoints.LatestForTask(ctx, ownerID, taskID)
		next := task.Goal
		if checkpoint != nil && strings.TrimSpace(checkpoint.NextAction) != "" {
			next = checkpoint.NextAction
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskQueued, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskQueued, task.Version+1, now
		_, _ = repos.WorkTaskInbox.CancelQueuedByKind(ctx, ownerID, taskID, store.InboxKindPause)
		_, _ = repos.WorkTaskInbox.CancelQueuedByKind(ctx, ownerID, taskID, store.InboxKindCancel)
		_, _ = updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`),
			"recovering", "从检查点恢复", next)
		mode := "readonly"
		if task.Profile != store.WorkTaskProfileRead {
			mode = "manual"
		}
		inbox := &store.WorkTaskInbox{
			OwnerUserID: ownerID, TaskID: taskID, SourceKind: store.InboxSourceControl,
			Kind: store.InboxKindRecover, Priority: 95, Body: next,
			Payload:        mustJSON(map[string]any{"checkpoint_id": checkpointID(checkpoint), "mode": mode}),
			IdempotencyKey: "recover:" + taskID + ":" + fmt.Sprint(task.Version),
		}
		created, item, err := repos.WorkTaskInbox.Add(ctx, inbox)
		if err != nil {
			return err
		}
		if !created {
			item, _ = repos.WorkTaskInbox.GetByIdempotencyKey(ctx, ownerID, inbox.IdempotencyKey)
		}
		job, err := createJobTx(ctx, repos, task, "从检查点恢复："+next,
			"task:"+taskID+":recover:"+fmt.Sprint(task.Version), "", "", task.CurrentSessionID, now)
		if err != nil {
			return err
		}
		result = &MessageResult{Task: task, Inbox: item, Job: job, Created: created, Delivered: "new_segment"}
		return appendTaskEvent(ctx, repos, ownerID, "task.recovered", task, map[string]any{
			"status": task.Status, "job_id": job.ID, "mode": mode,
		})
	})
	if err == nil && result != nil && result.Job != nil {
		s.enqueue(ctx, ownerID, taskID, result.Job.ID)
	}
	return result, err
}

func (s *Service) Reopen(ctx context.Context, ownerID, taskID string, expectedVersion int) (*store.WorkTask, error) {
	now := s.now()
	var updated *store.WorkTask
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskActive) {
			return fmt.Errorf("%w: 当前状态 %s 不能重新打开", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskActive, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskActive, task.Version+1, now
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.reopened", task, map[string]any{"status": task.Status})
	})
	return updated, err
}

// Wait 把执行段置于等待并释放 Worker 槽位；回调通过 AddMessage 创建新执行段。
func (s *Service) Wait(ctx context.Context, ownerID, taskID, jobID, sessionID string, expectedVersion int,
	waitingKind, summary, nextAction string, payload json.RawMessage) (*store.WorkTask, error) {
	status := store.WorkTaskWaitingUser
	switch waitingKind {
	case store.WaitingApproval:
		status = store.WorkTaskWaitingApproval
	case store.WaitingDevice:
		status = store.WorkTaskWaitingDevice
	case store.WaitingExternal:
		status = store.WorkTaskWaitingExternal
	case store.WaitingOther, store.WaitingTimer:
		status = store.WorkTaskWaitingOther
	}
	now := s.now()
	var updated *store.WorkTask
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, status) {
			return fmt.Errorf("%w: 当前状态 %s 不能进入等待", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, status, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = status, task.Version+1, now
		if _, err := updateLive(ctx, repos, task, waitingKind, payload, "waiting", summary, nextAction); err != nil {
			return err
		}
		err = repos.Checkpoints.Append(ctx, &store.WorkTaskCheckpoint{
			OwnerUserID: ownerID, TaskID: taskID, JobID: jobID, SessionID: sessionID,
			TaskVersion: task.Version, Kind: store.CheckpointWaiting,
			Summary: summary, NextAction: nextAction,
			Payload:   mustJSON(map[string]any{"waiting_kind": waitingKind, "payload": payload}),
			CreatedAt: now,
		})
		if err != nil {
			return err
		}
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.waiting", task, map[string]any{
			"status": status, "waiting_kind": waitingKind, "job_id": jobID,
		})
	})
	return updated, err
}

// SetActive 在执行段开始时把 queued 任务推进到 active。
func (s *Service) SetActive(ctx context.Context, ownerID, taskID, jobID, sessionID string) (*store.WorkTask, error) {
	now := s.now()
	var updated *store.WorkTask
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if task.Status == store.WorkTaskActive {
			_ = repos.WorkTasks.SetCurrentJob(ctx, ownerID, taskID, jobID, sessionID, now)
			updated = task
			return nil
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskActive) {
			return fmt.Errorf("%w: 状态 %s 不能开始执行", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskActive, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskActive, task.Version+1, now
		if err := repos.WorkTasks.SetCurrentJob(ctx, ownerID, taskID, jobID, sessionID, now); err != nil {
			return err
		}
		task.CurrentJobID, task.CurrentSessionID = jobID, sessionID
		_, _ = updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`), "executing", "Worker 开始执行", "")
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.active", task, map[string]any{
			"job_id": jobID, "session_id": sessionID,
		})
	})
	return updated, err
}

// RecordContextReceipt 保存本次执行使用的上下文来源、版本与截断原因。
func (s *Service) RecordContextReceipt(ctx context.Context, ownerID, taskID, jobID string, receipts json.RawMessage) error {
	if len(receipts) == 0 {
		receipts = json.RawMessage(`[]`)
	}
	repos := store.NewRepos(s.DB)
	_, err := repos.AgentEvents.Append(ctx, &store.AgentEvent{
		OwnerUserID: ownerID, Kind: "task.context.receipt", EntityType: "work_task",
		EntityID: taskID, Payload: mustJSON(map[string]any{
			"job_id": jobID, "receipts": receipts,
		}),
	})
	return err
}

// CheckpointTurn 由 Supervisor 在安全步骤边界写入。
func (s *Service) CheckpointTurn(ctx context.Context, ownerID, taskID, jobID, sessionID string,
	taskVersion int, kind, summary, nextAction string, payload json.RawMessage) error {
	return store.NewRepos(s.DB).Checkpoints.Append(ctx, &store.WorkTaskCheckpoint{
		OwnerUserID: ownerID, TaskID: taskID, JobID: jobID, SessionID: sessionID,
		TaskVersion: taskVersion, Kind: kind, Summary: summary, NextAction: nextAction,
		Payload: mustJSON(map[string]any{"value": payload}),
	})
}

// PendingAcceptance 在 Worker 产生交付物后进入验收；无 Artifact 时也可提交结果摘要。
func (s *Service) PendingAcceptance(ctx context.Context, ownerID, taskID, jobID string,
	expectedVersion int, summary string) (*store.WorkTask, error) {
	now := s.now()
	var updated *store.WorkTask
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskPendingAccept) {
			return fmt.Errorf("%w: 状态 %s 不能进入验收", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskPendingAccept, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskPendingAccept, task.Version+1, now
		_, _ = updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`), "pending_acceptance", summary, "等待用户验收")
		_ = repos.Checkpoints.Append(ctx, &store.WorkTaskCheckpoint{
			OwnerUserID: ownerID, TaskID: taskID, JobID: jobID,
			TaskVersion: task.Version, Kind: store.CheckpointTurnDone,
			Summary: summary, NextAction: "等待验收",
			Payload: mustJSON(map[string]any{"summary": summary}),
		})
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.pending_acceptance", task, map[string]any{
			"job_id": jobID, "summary": summary,
		})
	})
	return updated, err
}

func (s *Service) Complete(ctx context.Context, ownerID, taskID string, expectedVersion int) (*store.WorkTask, error) {
	now := s.now()
	var updated *store.WorkTask
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && task.Version != expectedVersion {
			return fmt.Errorf("%w: task version %d != %d", ErrConflict, task.Version, expectedVersion)
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskCompleted) {
			return fmt.Errorf("%w: 状态 %s 不能完成", ErrConflict, task.Status)
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskCompleted, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: 任务状态已变化", ErrConflict)
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskCompleted, task.Version+1, now
		task.CompletedAt = &now
		_, _ = updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`), "done", "任务已完成", "")
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.completed", task, map[string]any{"status": task.Status})
	})
	return updated, err
}

func (s *Service) Fail(ctx context.Context, ownerID, taskID, jobID, reason string) (*store.WorkTask, error) {
	now := s.now()
	var updated *store.WorkTask
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		task, err := repos.WorkTasks.Get(ctx, ownerID, taskID)
		if err != nil {
			return err
		}
		if !store.ValidWorkTaskTransition(task.Status, store.WorkTaskFailed) {
			return nil
		}
		changed, err := repos.WorkTasks.Transition(ctx, ownerID, taskID, task.Version, store.WorkTaskFailed, now)
		if err != nil || !changed {
			return err
		}
		task.Status, task.Version, task.UpdatedAt = store.WorkTaskFailed, task.Version+1, now
		_, _ = updateLive(ctx, repos, task, store.WaitingNone, json.RawMessage(`{}`), "failed", reason, "需要人工恢复")
		updated = task
		return appendTaskEvent(ctx, repos, ownerID, "task.failed", task, map[string]any{
			"job_id": jobID, "error": reason,
		})
	})
	return updated, err
}

func (s *Service) AddThread(ctx context.Context, ownerID, taskID, threadID, relation, reason string) error {
	relation = strings.TrimSpace(relation)
	if relation == "" {
		relation = store.WorkTaskRelationRelated
	}
	if !validRelation(relation) {
		return fmt.Errorf("%w: relation 无效", ErrInvalid)
	}
	repos := store.NewRepos(s.DB)
	if _, err := repos.WorkTasks.Get(ctx, ownerID, taskID); err != nil {
		return err
	}
	if _, err := repos.Threads.Get(ctx, ownerID, threadID); err != nil {
		return fmt.Errorf("%w: Thread 不存在", ErrInvalid)
	}
	return repos.WorkTaskThreads.Add(ctx, &store.WorkTaskThread{
		OwnerUserID: ownerID, TaskID: taskID, ThreadID: threadID, Relation: relation,
		Confidence: 1, Reason: strings.TrimSpace(reason), CreatedBy: "user",
	})
}

func (s *Service) RemoveThread(ctx context.Context, ownerID, taskID, threadID, relation string) (bool, error) {
	return store.NewRepos(s.DB).WorkTaskThreads.Remove(ctx, ownerID, taskID, threadID, relation)
}

func validRelation(relation string) bool {
	switch relation {
	case store.WorkTaskRelationOrigin, store.WorkTaskRelationRelated, store.WorkTaskRelationOutput:
		return true
	}
	return false
}

func createJobTx(ctx context.Context, repos *store.Repos, task *store.WorkTask, goal, idempotencyKey,
	threadID, triggerID, sessionID string, now time.Time) (*store.AgentJob, error) {
	active, err := repos.AgentJobs.ActivePrimaryForTask(ctx, task.OwnerUserID, task.ID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}
	job := &store.AgentJob{
		ID: uuid.NewString(), OwnerUserID: task.OwnerUserID, AgentID: store.AgentIDSuperAgent,
		Task: truncate(goal, 12000), Status: store.AgentJobQueued,
		IdempotencyKey: idempotencyKey, ThreadID: threadID, ParentTriggerID: triggerID,
		SessionID: sessionID, Profile: task.Profile, Priority: task.Priority,
		WorkTaskID: task.ID, JobRole: store.AgentJobRolePrimary, CreatedAt: now, UpdatedAt: now,
	}
	if err := repos.AgentJobs.Create(ctx, job); err != nil {
		if existing, getErr := repos.AgentJobs.GetByIdempotencyKey(ctx, task.OwnerUserID, idempotencyKey); getErr == nil {
			return existing, nil
		}
		return nil, err
	}
	if err := repos.AgentJobs.AddEvent(ctx, job.ID, "queued", "执行段已入队", now); err != nil {
		return nil, err
	}
	if err := repos.WorkTasks.SetCurrentJob(ctx, task.OwnerUserID, task.ID, job.ID, sessionID, now); err != nil {
		return nil, err
	}
	task.CurrentJobID, task.CurrentSessionID = job.ID, sessionID
	return job, nil
}

func updateLive(ctx context.Context, repos *store.Repos, task *store.WorkTask, waitingKind string,
	payload json.RawMessage, stage, summary, nextAction string) (bool, error) {
	live, err := repos.WorkTaskLive.Get(ctx, task.OwnerUserID, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		live = &store.WorkTaskLiveState{TaskID: task.ID, OwnerUserID: task.OwnerUserID}
	} else if err != nil {
		return false, err
	}
	live.Stage = stage
	live.Summary = truncate(summary, 2000)
	live.NextAction = truncate(nextAction, 2000)
	live.WaitingKind = waitingKind
	live.WaitingPayload = payload
	return repos.WorkTaskLive.Update(ctx, live, live.Version, time.Now())
}

func appendTaskEvent(ctx context.Context, repos *store.Repos, ownerID, kind string, task *store.WorkTask, payload map[string]any) error {
	if task == nil {
		return nil
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["task_id"] = task.ID
	payload["title"] = task.Title
	payload["version"] = task.Version
	_, err := repos.AgentEvents.Append(ctx, &store.AgentEvent{
		OwnerUserID: ownerID, Kind: kind, EntityType: "work_task", EntityID: task.ID,
		Payload: mustJSON(payload),
	})
	return err
}

func (s *Service) enqueue(ctx context.Context, ownerID, taskID, jobID string) {
	if s.OnEnqueue != nil {
		s.OnEnqueue(ctx, ownerID, taskID, jobID)
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func jobID(job *store.AgentJob) string {
	if job == nil {
		return ""
	}
	return job.ID
}

func checkpointID(c *store.WorkTaskCheckpoint) string {
	if c == nil {
		return ""
	}
	return c.ID
}
