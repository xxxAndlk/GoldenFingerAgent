// Package task: the 8-state task state machine and intent≠fact reminder policy.
package task

import (
	"context"
	"fmt"

	"goldenfinger/agent/internal/store"
)

// ErrIllegalTransition is returned for a state change outside the transition table.
var ErrIllegalTransition = fmt.Errorf("task: illegal transition")

// legalTransitions encodes the doc's state machine (see requirements §7).
// done / cancelled / expired are terminal.
var legalTransitions = map[string]map[string]bool{
	store.TaskDraft: {
		store.TaskPendingConfirm: true,
		store.TaskScheduled:      true, // auto path (score >= threshold)
		store.TaskDone:           true,
		store.TaskCancelled:      true,
	},
	store.TaskPendingConfirm: {
		store.TaskScheduled: true, // user confirmed
		store.TaskDone:      true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskScheduled: {
		store.TaskNotified:  true, // reminder sent
		store.TaskDone:      true,
		store.TaskSnoozed:   true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskNotified: {
		store.TaskNotified:  true, // escalation re-notify
		store.TaskDone:      true,
		store.TaskSnoozed:   true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskSnoozed: {
		store.TaskScheduled: true, // new fire time
		store.TaskDone:      true,
		store.TaskCancelled: true,
		store.TaskExpired:   true,
	},
	store.TaskDone:      {},
	store.TaskCancelled: {},
	store.TaskExpired:   {},
}

// CanTransition reports whether from→to is legal (pure, testable).
func CanTransition(from, to string) bool {
	return legalTransitions[from][to]
}

// Transition validates and persists a state change via CAS.
// mutate may adjust abs_time/deadline (e.g. snooze) before the write.
func Transition(ctx context.Context, repo *store.TaskRepo, id, from, to string, mutate func(*store.Task)) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s → %s", ErrIllegalTransition, from, to)
	}
	return repo.Transition(ctx, id, from, to, mutate)
}
