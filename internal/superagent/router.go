package superagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"goldenfinger/agent/internal/store"
)

type Action string

const (
	ActionIgnore        Action = "ignore"
	ActionNotify        Action = "notify"
	ActionReply         Action = "reply"
	ActionCreateWorker  Action = "create_worker"
	ActionRouteToWorker Action = "route_to_worker"
	ActionAskUser       Action = "ask_user"
)

// Decision 是 Router 对一条 Trigger 的行动建议。Worker 内部步骤不进入这里。
type Decision struct {
	Action           Action `json:"action"`
	TriggerID        string `json:"trigger_id"`
	ThreadID         string `json:"thread_id,omitempty"`
	WorkerTaskID     string `json:"worker_task_id,omitempty"`
	Goal             string `json:"goal,omitempty"`
	Profile          string `json:"profile,omitempty"`
	Priority         int    `json:"priority,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
	Reply            string `json:"reply,omitempty"`
	Question         string `json:"question,omitempty"`
	Reason           string `json:"reason,omitempty"`
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
	case ActionCreateWorker:
		if strings.TrimSpace(d.Goal) == "" {
			return errors.New("create_worker requires goal")
		}
		if d.Priority < 0 || d.Priority > 9 {
			return errors.New("priority must be 0..9")
		}
	case ActionRouteToWorker:
		if strings.TrimSpace(d.WorkerTaskID) == "" {
			return errors.New("route_to_worker requires worker_task_id")
		}
	default:
		return fmt.Errorf("unsupported action %q", d.Action)
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
	workerTaskID := decision.WorkerTaskID
	if decision.Action == ActionCreateWorker || decision.Action == ActionRouteToWorker {
		if s.Workers == nil {
			decision.Action = ActionNotify
			decision.Reason = joinReason(decision.Reason, "WorkerManager 未启用，降级为通知")
		} else if decision.Action == ActionCreateWorker {
			id, err := s.Workers.CreateWorker(ctx, ownerID, trigger, decision)
			if err != nil {
				return RouteResult{}, err
			}
			workerTaskID = id
		} else {
			if err := s.Workers.RouteToWorker(ctx, ownerID, trigger, decision); err != nil {
				return RouteResult{}, err
			}
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
		})
	})
	if err != nil {
		return RouteResult{}, err
	}
	if s.OnNotify != nil && (decision.Action == ActionNotify || decision.Action == ActionReply || decision.Action == ActionAskUser) {
		s.OnNotify(ctx, ownerID, trigger, decision)
	}
	return RouteResult{
		TriggerID:    trigger.ID,
		Action:       decision.Action,
		Status:       store.TriggerStatusRouted,
		WorkerTaskID: workerTaskID,
	}, nil
}

func joinReason(a, b string) string {
	if strings.TrimSpace(a) == "" {
		return b
	}
	return a + "；" + b
}
