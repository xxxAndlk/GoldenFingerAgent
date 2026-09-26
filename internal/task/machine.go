// Package task：8 状态的任务状态机与 intent≠fact 的提醒策略。
package task

import (
	"context"
	"fmt"

	"goldenfinger/agent/internal/store"
)

// ErrIllegalTransition 在状态变更超出转换表时返回。
var ErrIllegalTransition = fmt.Errorf("task: illegal transition")

// legalTransitions 编码文档中的状态机（见需求 §7）。
// done / cancelled / expired 为终态。
var legalTransitions = map[string]map[string]bool{
	store.TaskDraft: {
		store.TaskPendingConfirm: true,
		store.TaskScheduled:      true, // 自动路径（分数 >= 阈值）
		store.TaskDone:           true,
		store.TaskCancelled:      true,
	},
	store.TaskPendingConfirm: {
		store.TaskScheduled: true, // 用户已确认
		store.TaskDone:      true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskScheduled: {
		store.TaskNotified:  true, // 提醒已发送
		store.TaskDone:      true,
		store.TaskSnoozed:   true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskNotified: {
		store.TaskNotified:  true, // 升级重提醒
		store.TaskDone:      true,
		store.TaskSnoozed:   true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskSnoozed: {
		store.TaskScheduled: true, // 新的触发时间
		store.TaskDone:      true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskDone:      {},
	store.TaskCancelled: {},
	store.TaskExpired:   {},
}

// CanTransition 报告 from→to 是否合法（纯函数，可测试）。
func CanTransition(from, to string) bool {
	return legalTransitions[from][to]
}

// Transition 通过 CAS 校验并持久化一次状态变更。
// mutate 可在写入前调整 abs_time/deadline（例如推迟）。
func Transition(ctx context.Context, repo *store.TaskRepo, id, from, to string, mutate func(*store.Task)) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s → %s", ErrIllegalTransition, from, to)
	}
	return repo.Transition(ctx, id, from, to, mutate)
}
