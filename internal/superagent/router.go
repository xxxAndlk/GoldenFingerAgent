package superagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

type Action string

const (
	ActionIgnore        Action = "ignore"
	ActionNotify        Action = "notify"
	ActionReply         Action = "reply"
	ActionCreateWorker  Action = "create_worker"
	ActionRouteToWorker Action = "route_to_worker"
	ActionAskUser       Action = "ask_user"
	ActionCreateTask    Action = "create_task"
	ActionUpdateTask    Action = "update_task"
	ActionPrepareTask   Action = "prepare_task"
	ActionPauseTask     Action = "pause_task"
	ActionResumeTask    Action = "resume_task"
	ActionCancelTask    Action = "cancel_task"
)

var taskAssociationActions = map[Action]bool{
	ActionUpdateTask: true, ActionPrepareTask: true, ActionRouteToWorker: true,
	ActionPauseTask: true, ActionResumeTask: true, ActionCancelTask: true,
}

// Decision 是 Router 对一条 Trigger 的行动建议。Worker 内部步骤不进入这里。
type Decision struct {
	Action            Action  `json:"action"`
	TriggerID         string  `json:"trigger_id"`
	ThreadID          string  `json:"thread_id,omitempty"`
	WorkerTaskID      string  `json:"worker_task_id,omitempty"`
	WorkTaskID        string  `json:"work_task_id,omitempty"`
	Confidence        float64 `json:"confidence,omitempty"`
	AssociationReason string  `json:"association_reason,omitempty"`
	Goal              string  `json:"goal,omitempty"`
	Profile           string  `json:"profile,omitempty"`
	Priority          int     `json:"priority,omitempty"`
	RequiresApproval  bool    `json:"requires_approval,omitempty"`
	Reply             string  `json:"reply,omitempty"`
	Question          string  `json:"question,omitempty"`
	Reason            string  `json:"reason,omitempty"`
}

func (d Decision) Validate() error {
	switch d.Action {
	case ActionIgnore, ActionNotify:
	case ActionReply:
		if strings.TrimSpace(d.Reply) == "" {
			return errors.New("reply requires reply text")
		}
	case ActionAskUser:
		if strings.TrimSpace(d.Question) == "" {
			return errors.New("ask_user requires question")
		}
	case ActionCreateWorker, ActionCreateTask:
		if strings.TrimSpace(d.Goal) == "" {
			return errors.New("create_worker/create_task requires goal")
		}
		if d.Priority < 0 || d.Priority > 9 {
			return errors.New("priority must be 0..9")
		}
	case ActionRouteToWorker:
		if strings.TrimSpace(d.WorkTaskID) == "" && strings.TrimSpace(d.WorkerTaskID) == "" {
			return errors.New("route_to_worker requires work_task_id or worker_task_id")
		}
	case ActionUpdateTask, ActionPrepareTask, ActionPauseTask, ActionResumeTask, ActionCancelTask:
		if strings.TrimSpace(d.WorkTaskID) == "" {
			return fmt.Errorf("%s requires work_task_id", d.Action)
		}
	default:
		return fmt.Errorf("unsupported action %q", d.Action)
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return errors.New("confidence must be 0..1")
	}
	return nil
}

type Router interface {
	Decide(ctx context.Context, ownerID string, triggers []store.Trigger) ([]Decision, error)
}

// DeterministicRouter 是模型不可用时的回退：只发低打扰通知，不执行任务。
type DeterministicRouter struct{}

func (DeterministicRouter) Decide(_ context.Context, _ string, triggers []store.Trigger) ([]Decision, error) {
	out := make([]Decision, 0, len(triggers))
	for i := range triggers {
		out = append(out, Decision{
			Action:    ActionNotify,
			TriggerID: triggers[i].ID,
			ThreadID:  triggers[i].ThreadID,
			Reason:    "确定性回退：未启用模型 Router",
		})
	}
	return out, nil
}

// WorkerSink 由 WorkerManager 实现；未配置时 Route 会把相关决定降级为 notify。
type WorkerSink interface {
	CreateWorker(ctx context.Context, ownerID string, trigger *store.Trigger, d Decision) (string, error)
	RouteToWorker(ctx context.Context, ownerID string, trigger *store.Trigger, d Decision) error
}

type RouteResult struct {
	TriggerID    string `json:"trigger_id"`
	Action       Action `json:"action"`
	Status       string `json:"status"`
	WorkerTaskID string `json:"worker_task_id,omitempty"`
	WorkTaskID   string `json:"work_task_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

// RoutePendingForAll 是后台单大脑循环：按 owner 逐个路由 queued Trigger。
func (s *Service) RoutePendingForAll(ctx context.Context, router Router, perOwner int) ([]RouteResult, error) {
	users, err := store.NewRepos(s.DB).Users.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	var out []RouteResult
	for _, user := range users {
		results, err := s.RoutePending(ctx, user.ID, router, perOwner)
		if err != nil {
			return out, err
		}
		out = append(out, results...)
	}
	return out, nil
}

// RoutePending 取一批 queued Trigger，交给 Router 决策并落库。
func (s *Service) RoutePending(ctx context.Context, ownerID string, router Router, limit int) ([]RouteResult, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	triggers, err := s.DB.Triggers().List(ctx, ownerID, store.TriggerStatusQueued, "", limit)
	if err != nil {
		return nil, err
	}
	if len(triggers) == 0 {
		return []RouteResult{}, nil
	}
	return s.Route(ctx, ownerID, triggers, router)
}

func (s *Service) Route(ctx context.Context, ownerID string, triggers []store.Trigger, router Router) ([]RouteResult, error) {
	if router == nil {
		router = DeterministicRouter{}
	}
	decisions, err := router.Decide(ctx, ownerID, triggers)
	if err != nil {
		decisions, _ = DeterministicRouter{}.Decide(ctx, ownerID, triggers)
	}
	byTrigger := make(map[string]Decision, len(decisions))
	for _, d := range decisions {
		byTrigger[d.TriggerID] = d
	}

	out := make([]RouteResult, 0, len(triggers))
	for i := range triggers {
		trigger := &triggers[i]
		decision, ok := byTrigger[trigger.ID]
		if !ok {
			decision = Decision{Action: ActionNotify, TriggerID: trigger.ID, Reason: "Router 未返回决定，按通知处理"}
		}
		if err := decision.Validate(); err != nil {
			decision = Decision{Action: ActionNotify, TriggerID: trigger.ID, Reason: "Router 决定无效：" + err.Error()}
		}
		if decision.ThreadID == "" {
			decision.ThreadID = trigger.ThreadID
		}
		result, err := s.applyDecision(ctx, ownerID, trigger, decision)
		if err != nil {
			out = append(out, RouteResult{TriggerID: trigger.ID, Action: decision.Action, Status: store.TriggerStatusFailed, Error: err.Error()})
			continue
		}
		out = append(out, result)
	}
	return out, nil
}

func (s *Service) applyDecision(ctx context.Context, ownerID string, trigger *store.Trigger, decision Decision) (RouteResult, error) {
	workTaskID := strings.TrimSpace(decision.WorkTaskID)
	workerTaskID := decision.WorkerTaskID

	// 低置信度关联不得直接改写已有任务，先向用户澄清。
	if taskAssociationActions[decision.Action] && decision.Confidence > 0 && decision.Confidence < 0.6 {
		decision.Action = ActionAskUser
		decision.Question = "这条事件可能和已有任务有关；请确认要更新哪个任务。"
		decision.Reason = joinReason(decision.Reason, "低置信度关联，转为澄清")
	}

	// 模型只能选择 TaskIndex 中的任务；服务端重新校验 owner 与状态。
	if taskAssociationActions[decision.Action] && workTaskID != "" {
		if s.Tasks == nil {
			return RouteResult{}, errors.New("Task 控制面未启用")
		}
		entry, err := s.taskIndexContains(ctx, ownerID, workTaskID)
		if err != nil {
			return RouteResult{}, err
		}
		if entry == nil {
			return RouteResult{}, errors.New("work_task_id 不在候选列表")
		}
	}

	if taskAssociationActions[decision.Action] && workTaskID == "" && workerTaskID != "" {
		if job, err := s.DB.AgentJobs().Get(ctx, ownerID, workerTaskID); err == nil {
			workTaskID = job.WorkTaskID
			decision.WorkTaskID = workTaskID
		}
	}

	switch decision.Action {
	case ActionCreateTask:
		if s.Tasks == nil {
			return RouteResult{}, errors.New("Task 控制面未启用")
		}
		task, err := s.Tasks.Create(ctx, worktask.CreateInput{
			OwnerID: ownerID, Title: firstText(trigger.Title, decision.Goal), Goal: decision.Goal,
			Priority: decision.Priority, Profile: decision.Profile, SourceTriggerID: trigger.ID,
			ThreadID: trigger.ThreadID, IdempotencyKey: "superagent:" + trigger.ID,
			Start: true, CreatedBy: "router", JobThreadID: trigger.ThreadID,
			JobParentTriggerID: trigger.ID,
		})
		if err != nil {
			return RouteResult{}, err
		}
		workTaskID = task.ID
	case ActionCreateWorker, ActionRouteToWorker:
		if s.Workers == nil {
			decision.Action = ActionNotify
			decision.Reason = joinReason(decision.Reason, "WorkerManager 未启用，降级为通知")
		} else if !s.autoWorkerEnabledFor(ctx, ownerID, trigger) {
			decision.Action = ActionNotify
			decision.Reason = joinReason(decision.Reason, "渠道或全局自动 Worker 已关闭，降级为通知")
		} else if decision.Action == ActionCreateWorker {
			id, err := s.Workers.CreateWorker(ctx, ownerID, trigger, decision)
			if err != nil {
				return RouteResult{}, err
			}
			workerTaskID = id
			if job, getErr := s.DB.AgentJobs().Get(ctx, ownerID, id); getErr == nil {
				workTaskID = job.WorkTaskID
			}
		} else if s.Tasks != nil && workTaskID != "" {
			if _, err := s.Tasks.AddThreadEvent(ctx, ownerID, workTaskID, trigger.ID,
				triggerEventText(trigger), "trigger:"+trigger.ID); err != nil {
				return RouteResult{}, err
			}
		} else {
			if err := s.Workers.RouteToWorker(ctx, ownerID, trigger, decision); err != nil {
				return RouteResult{}, err
			}
		}
	case ActionUpdateTask, ActionPrepareTask:
		if s.Tasks == nil {
			return RouteResult{}, errors.New("Task 控制面未启用")
		}
		if workTaskID == "" {
			return RouteResult{}, errors.New("缺少 work_task_id")
		}
		if _, err := s.Tasks.AddThreadEvent(ctx, ownerID, workTaskID, trigger.ID,
			triggerEventText(trigger), "trigger:"+trigger.ID); err != nil {
			return RouteResult{}, err
		}
	case ActionPauseTask, ActionResumeTask, ActionCancelTask:
		if s.Tasks == nil {
			return RouteResult{}, errors.New("Task 控制面未启用")
		}
		if workTaskID == "" {
			return RouteResult{}, errors.New("缺少 work_task_id")
		}
		var err error
		switch decision.Action {
		case ActionPauseTask:
			_, err = s.Tasks.Pause(ctx, ownerID, workTaskID, -1)
		case ActionResumeTask:
			_, err = s.Tasks.Resume(ctx, ownerID, workTaskID, -1)
		case ActionCancelTask:
			_, err = s.Tasks.Cancel(ctx, ownerID, workTaskID, -1)
		}
		if err != nil {
			return RouteResult{}, err
		}
	}

	routeResult := mustJSON(decision)
	now := s.now()
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		changed, err := repos.Triggers.Transition(ctx, ownerID, trigger.ID,
			[]string{store.TriggerStatusQueued}, store.TriggerStatusRouted, routeResult, "", now)
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		return appendEvent(ctx, repos, ownerID, "trigger.routed", "trigger", trigger.ID, map[string]any{
			"action":         decision.Action,
			"reason":         decision.Reason,
			"worker_task_id": workerTaskID,
			"work_task_id":   workTaskID,
		})
	})
	if err != nil {
		return RouteResult{}, err
	}
	if s.OnNotify != nil && (decision.Action == ActionNotify || decision.Action == ActionReply || decision.Action == ActionAskUser) &&
		s.notifyEnabled(ctx, ownerID) {
		s.OnNotify(ctx, ownerID, trigger, decision)
	}
	return RouteResult{
		TriggerID:    trigger.ID,
		Action:       decision.Action,
		Status:       store.TriggerStatusRouted,
		WorkerTaskID: workerTaskID,
		WorkTaskID:   workTaskID,
	}, nil
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func triggerEventText(trigger *store.Trigger) string {
	if trigger == nil {
		return ""
	}
	title := strings.TrimSpace(trigger.Title)
	body := strings.TrimSpace(trigger.Body)
	switch {
	case title != "" && body != "" && title != body:
		return title + "：" + body
	case title != "":
		return title
	default:
		return body
	}
}

func joinReason(a, b string) string {
	if strings.TrimSpace(a) == "" {
		return b
	}
	return a + "；" + b
}
