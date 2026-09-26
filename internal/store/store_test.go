package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

// padVec 把短测试向量填充到 schema 维度（VECTOR(1024)）。
func padVec(head ...float32) []float32 {
	v := make([]float32, 1024)
	copy(v, head)
	return v
}

func jsonEqual(a, b any) bool { return reflect.DeepEqual(a, b) }

func testDB(t *testing.T) *store.DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping store integration tests")
	}
	ctx := context.Background()
	db, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestMigrateAndUserRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db.Pool)

	u := &store.User{UserType: store.UserGeneral, Name: "测试用户", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	got, err := repos.Users.Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.Name != "测试用户" || got.UserType != store.UserGeneral {
		t.Fatalf("unexpected user: %+v", got)
	}
}

func TestReminderIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db.Pool)

	u := &store.User{UserType: store.UserGeneral, Name: "rem-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		OwnerUserID: u.ID,
		Kind:        store.KindAlarm,
		Status:      store.TaskScheduled,
		Schema:      json.RawMessage(`{"title":"test"}`),
	}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	rem := &store.Reminder{
		TaskID:    task.ID,
		FireAt:    time.Now().Add(time.Minute),
		Channel:   "app",
		Level:     1,
		DedupeKey: "task:" + task.ID + ":L1:123",
	}
	created, err := repos.Reminders.InsertIdempotent(ctx, rem)
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}
	rem2 := *rem
	rem2.ID = ""
	created2, err := repos.Reminders.InsertIdempotent(ctx, &rem2)
	if err != nil {
		t.Fatalf("second insert err: %v", err)
	}
	if created2 {
		t.Fatal("second insert with same dedupe_key must be a no-op")
	}
}

func TestTaskTransitionCAS(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db.Pool)

	u := &store.User{UserType: store.UserGeneral, Name: "cas-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	task := &store.Task{OwnerUserID: u.ID, Kind: store.KindIntent, Status: store.TaskDraft}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := repos.Tasks.Transition(ctx, task.ID, store.TaskDraft, store.TaskScheduled, nil); err != nil {
		t.Fatalf("legal transition failed: %v", err)
	}
	// 错误的起始状态必须冲突。
	err := repos.Tasks.Transition(ctx, task.ID, store.TaskDraft, store.TaskDone, nil)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	// 正确的 CAS 得以继续。
	if err := repos.Tasks.Transition(ctx, task.ID, store.TaskScheduled, store.TaskDone, nil); err != nil {
		t.Fatalf("second transition: %v", err)
	}
}

func TestFactSimilarAndForgetCascade(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db.Pool)

	u := &store.User{UserType: store.UserGeneral, Name: "fact-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	p := &store.Person{OwnerUserID: u.ID, CanonicalName: "张阿姨"}
	if err := repos.Persons.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := repos.Persons.AddAlias(ctx, p.ID, "阿姨张"); err != nil {
		t.Fatal(err)
	}

	f := &store.Fact{
		PersonID:   p.ID,
		FactType:   "contact",
		ValueText:  "电话 13800000000",
		Confidence: 0.9,
		Status:     store.FactConfirmed,
		Embedding:  padVec(1, 0, 0),
		ValidFrom:  time.Now(),
	}
	if err := repos.Facts.Insert(ctx, f); err != nil {
		t.Fatalf("insert fact: %v", err)
	}

	hits, err := repos.Facts.Similar(ctx, u.ID, padVec(1, 0, 0), 5)
	if err != nil {
		t.Fatalf("similar: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected similarity hit")
	}

	// 遗忘级联：人物软删除，别名与事实硬删除。
	if err := repos.Persons.SoftDeleteCascade(ctx, u.ID, p.ID); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := repos.Persons.Get(ctx, u.ID, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("person must be gone, got %v", err)
	}
	hits, err = repos.Facts.Similar(ctx, u.ID, padVec(1, 0, 0), 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.PersonID == p.ID {
			t.Fatal("fact of forgotten person must not be retrievable")
		}
	}
}

func TestSessionStateAndMessages(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db.Pool)

	u := &store.User{UserType: store.UserGeneral, Name: "sess-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	s := &store.ChatSession{OwnerUserID: u.ID, State: json.RawMessage(`{}`)}
	if err := repos.Sessions.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	pending := json.RawMessage(`{"pending_action":{"type":"task_create"}}`)
	if err := repos.Sessions.SaveState(ctx, s.ID, pending); err != nil {
		t.Fatal(err)
	}
	got, err := repos.Sessions.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var want, have any
	if err := json.Unmarshal(pending, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.State, &have); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(want, have) {
		t.Fatalf("state mismatch: %s", got.State)
	}

	for i, role := range []string{"user", "assistant"} {
		m := &store.ChatMessage{SessionID: s.ID, Role: role, Content: string(rune('a' + i))}
		if err := repos.Sessions.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := repos.Sessions.RecentMessages(ctx, s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}
