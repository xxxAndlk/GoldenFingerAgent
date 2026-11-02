package superagent_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

var testNow = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func newTestService(t *testing.T) (*superagent.Service, *store.DB, *store.User) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	u := &store.User{UserType: store.UserGeneral, Name: "superagent-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	svc := superagent.New(db)
	svc.Now = func() time.Time { return testNow }
	return svc, db, u
}

func eventCount(t *testing.T, db *store.DB, ownerID string) int {
	t.Helper()
	events, err := db.AgentEvents().After(context.Background(), ownerID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}

func TestIngestCreatesThreadAndDedupes(t *testing.T) {
	svc, db, u := newTestService(t)
	ctx := context.Background()
	in := superagent.TriggerInput{
		Channel: "wechat", ExternalID: "msg-1", ThreadExternalID: "chat-1",
		Kind: "message", Title: "项目群", Body: "周五能交付吗",
	}
	first, err := svc.Ingest(ctx, u.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Trigger.Status != store.TriggerStatusQueued {
		t.Fatalf("first ingest: %+v", first)
	}
	if first.Thread == nil || first.Thread.ID == "" {
		t.Fatal("thread must be created")
	}
	if got := eventCount(t, db, u.ID); got != 2 {
		t.Fatalf("events=%d want 2 (created+queued)", got)
	}

	second, err := svc.Ingest(ctx, u.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created {
		t.Fatal("duplicate delivery must not create a new trigger")
	}
	if second.Trigger.ID != first.Trigger.ID {
		t.Fatalf("duplicate id=%s want=%s", second.Trigger.ID, first.Trigger.ID)
	}
	if got := eventCount(t, db, u.ID); got != 2 {
		t.Fatalf("duplicate added events: %d", got)
	}
	threads, err := svc.ListThreads(ctx, u.ID, "", 10)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads=%v err=%v", threads, err)
	}
}

func TestFilterIgnoresBeforeQueue(t *testing.T) {
	svc, db, u := newTestService(t)
	svc.Filter.EnabledChannels = map[string]bool{"email": true}
	result, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "wechat", ExternalID: "msg-filter", Kind: "message", Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Filtered || result.Trigger.Status != store.TriggerStatusIgnored {
		t.Fatalf("result=%+v", result)
	}
	if got := eventCount(t, db, u.ID); got != 2 {
		t.Fatalf("events=%d want 2 (created+ignored)", got)
	}
}

func TestRoutePendingNotifiesAndWritesEvent(t *testing.T) {
	svc, db, u := newTestService(t)
	var notified atomic.Int32
	svc.OnNotify = func(context.Context, string, *store.Trigger, superagent.Decision) {
		notified.Add(1)
	}
	if _, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "notification", ExternalID: "n-1", Kind: "notification", Title: "提醒",
	}); err != nil {
		t.Fatal(err)
	}
	results, err := svc.RoutePending(context.Background(), u.ID, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != superagent.ActionNotify || results[0].Status != store.TriggerStatusRouted {
		t.Fatalf("results=%+v", results)
	}
	if notified.Load() != 1 {
		t.Fatalf("notify callback=%d", notified.Load())
	}
	triggers, err := svc.ListTriggers(context.Background(), u.ID, store.TriggerStatusRouted, "", 10)
	if err != nil || len(triggers) != 1 {
		t.Fatalf("routed triggers=%v err=%v", triggers, err)
	}
	if got := eventCount(t, db, u.ID); got != 3 {
		t.Fatalf("events=%d want 3 (created+queued+routed)", got)
	}
}

type workerRouter struct{}

func (workerRouter) Decide(_ context.Context, _ string, triggers []store.Trigger) ([]superagent.Decision, error) {
	out := make([]superagent.Decision, 0, len(triggers))
	for _, trigger := range triggers {
		out = append(out, superagent.Decision{
			Action: superagent.ActionCreateWorker, TriggerID: trigger.ID, Goal: "draft reply",
		})
	}
	return out, nil
}

type countingWorkers struct{ created int }

func (w *countingWorkers) CreateWorker(context.Context, string, *store.Trigger, superagent.Decision) (string, error) {
	w.created++
	return "worker-1", nil
}
func (w *countingWorkers) RouteToWorker(context.Context, string, *store.Trigger, superagent.Decision) error {
	w.created++
	return nil
}

func TestRouteAutoWorkerGateFallsBackToNotify(t *testing.T) {
	svc, _, u := newTestService(t)
	workers := &countingWorkers{}
	svc.Workers = workers
	svc.AutoWorkerAllowed = func(context.Context, string, string) (bool, error) { return false, nil }
	var notified atomic.Int32
	svc.OnNotify = func(context.Context, string, *store.Trigger, superagent.Decision) {
		notified.Add(1)
	}
	if _, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "mail-gate", Kind: "email", Title: "delivery",
	}); err != nil {
		t.Fatal(err)
	}
	results, err := svc.RoutePending(context.Background(), u.ID, workerRouter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if workers.created != 0 {
		t.Fatalf("worker must not be created: %d", workers.created)
	}
	if len(results) != 1 || results[0].Action != superagent.ActionNotify {
		t.Fatalf("results=%+v", results)
	}
	if notified.Load() != 1 {
		t.Fatalf("notify=%d", notified.Load())
	}
}

func TestNotifyGateSuppressesOutboxCallback(t *testing.T) {
	svc, _, u := newTestService(t)
	svc.NotifyAllowed = func(context.Context, string) (bool, error) { return false, nil }
	var notified atomic.Int32
	svc.OnNotify = func(context.Context, string, *store.Trigger, superagent.Decision) {
		notified.Add(1)
	}
	if _, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "notification", ExternalID: "notify-gate", Kind: "notification", Title: "提醒",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RoutePending(context.Background(), u.ID, nil, 10); err != nil {
		t.Fatal(err)
	}
	if notified.Load() != 0 {
		t.Fatalf("proactive notify must be suppressed, got %d", notified.Load())
	}
}

func TestRouteFallsBackWhenWorkerManagerMissing(t *testing.T) {
	svc, _, u := newTestService(t)
	if _, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "mail-1", Kind: "email", Title: "delivery",
	}); err != nil {
		t.Fatal(err)
	}
	results, err := svc.RoutePending(context.Background(), u.ID, workerRouter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != superagent.ActionNotify {
		t.Fatalf("results=%+v", results)
	}
}

func TestTriggerOwnerIsolation(t *testing.T) {
	svc, db, first := newTestService(t)
	repos := store.NewRepos(db)
	second := &store.User{UserType: store.UserGeneral, Name: "other", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	in := superagent.TriggerInput{
		Channel: "wechat", ExternalID: "same-id", ThreadExternalID: "same-chat",
		Kind: "message", Body: "hello",
	}
	a, err := svc.Ingest(context.Background(), first.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Ingest(context.Background(), second.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Created || !b.Created || a.Trigger.ID == b.Trigger.ID {
		t.Fatalf("owner isolation failed: a=%+v b=%+v", a.Trigger, b.Trigger)
	}
	if a.Thread.ID == b.Thread.ID {
		t.Fatal("threads must be owner-scoped")
	}
}
