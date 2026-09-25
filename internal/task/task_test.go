package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

// fakeQueue records scheduling calls.
type fakeQueue struct {
	enqueued  []string
	cancelled []string
}

func (f *fakeQueue) EnqueueForTask(ctx context.Context, t *store.Task) error {
	f.enqueued = append(f.enqueued, t.ID)
	return nil
}
func (f *fakeQueue) CancelForTask(ctx context.Context, taskID string) error {
	f.cancelled = append(f.cancelled, taskID)
	return nil
}

func TestTransitionTable(t *testing.T) {
	all := []string{
		store.TaskDraft, store.TaskPendingConfirm, store.TaskScheduled, store.TaskNotified,
		store.TaskDone, store.TaskSnoozed, store.TaskCancelled, store.TaskExpired,
	}
	// Legal edges from the doc's state machine.
	legal := map[string][]string{
		store.TaskDraft:          {store.TaskPendingConfirm, store.TaskScheduled, store.TaskDone, store.TaskCancelled},
		store.TaskPendingConfirm: {store.TaskScheduled, store.TaskDone, store.TaskCancelled, store.TaskExpired},
		store.TaskScheduled:      {store.TaskNotified, store.TaskDone, store.TaskSnoozed, store.TaskCancelled, store.TaskExpired},
		store.TaskNotified:       {store.TaskNotified, store.TaskDone, store.TaskSnoozed, store.TaskCancelled, store.TaskExpired},
		store.TaskSnoozed:        {store.TaskScheduled, store.TaskDone, store.TaskCancelled, store.TaskExpired},
	}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, ok := range legal[from] {
				if ok == to {
					want = true
				}
			}
			if CanTransition(from, to) != want {
				t.Errorf("CanTransition(%s→%s) = %v, want %v", from, to, !want, want)
			}
		}
	}
	// Terminal states.
	for _, terminal := range []string{store.TaskDone, store.TaskCancelled, store.TaskExpired} {
		for _, to := range all {
			if CanTransition(terminal, to) {
				t.Errorf("terminal %s must not transition to %s", terminal, to)
			}
		}
	}
}

func TestIllegalTransitionError(t *testing.T) {
	ctx := context.Background()
	// Transition with illegal edge short-circuits before touching the repo.
	err := Transition(ctx, nil, "some-id", store.TaskDone, store.TaskScheduled, nil)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("want ErrIllegalTransition, got %v", err)
	}
}

func TestIntentVsFactTemplates(t *testing.T) {
	task := &store.Task{Kind: store.KindIntent}
	got := BuildReminderText(task, 1, "订票")
	if got != "别忘了：订票" {
		t.Errorf("intent copy = %q (P3: must only say don't forget)", got)
	}

	fact := &store.Task{Kind: store.KindFact, EventTemplate: "trip"}
	got = BuildReminderText(fact, 1, "去上海")
	if got != "行程提醒：去上海" {
		t.Errorf("fact copy = %q", got)
	}

	alarm := &store.Task{Kind: store.KindAlarm}
	if got := BuildReminderText(alarm, 2, "吃药"); got != "⏰ 闹钟响啦：吃药" {
		t.Errorf("alarm copy = %q", got)
	}

	// Intents must NEVER get lead-time event reminders (P3: 没订票不提醒赶飞机).
	if leads := FactLeadReminders(""); len(leads) != 0 {
		t.Errorf("default template leads = %v", leads)
	}
	_ = fact
	intentLeads := FactLeadReminders("trip")
	if len(intentLeads) == 0 {
		t.Error("trip fact should have lead reminders")
	}
}

func TestDedupeKeys(t *testing.T) {
	at := time.Date(2026, 3, 6, 7, 0, 0, 0, time.UTC)
	k1 := DedupeKey("t1", 1, at)
	k2 := DedupeKey("t1", 1, at)
	if k1 != k2 {
		t.Error("dedupe key must be stable")
	}
	if DedupeKey("t1", 2, at) == k1 {
		t.Error("levels must differ")
	}
	day := time.Date(2026, 3, 6, 11, 0, 0, 0, time.UTC)
	if DigestDedupeKey("u1", day) != "digest:u1:2026-03-06" {
		t.Errorf("digest key = %q", DigestDedupeKey("u1", day))
	}
}
