package store_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func pendingItem(key, owner, kind, payload string) store.RemotePending {
	return store.RemotePending{
		IdempotencyKey: key,
		OwnerUserID:    owner,
		Kind:           kind,
		Payload:        json.RawMessage(payload),
		Status:         "pending",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
}

func TestRemotePendingEnqueueReturnsExistingState(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)

	first, inserted, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-1", "owner-a", "work_task_msg", `{"text":"继续"}`))
	if err != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	if first.Status != "pending" {
		t.Fatalf("status=%q", first.Status)
	}

	// 同 owner、同 kind、同 payload：返回原记录，不新增。
	again, inserted, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-1", "owner-a", "work_task_msg", `{"text":"继续"}`))
	if err != nil || inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	if again.IdempotencyKey != "key-1" || again.Status != "pending" {
		t.Fatalf("existing=%+v", again)
	}
	pending, err := repos.RemotePending.ListPending(ctx, "owner-a", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}

func TestRemotePendingEnqueueRejectsDifferentOwner(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)

	if _, _, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-shared", "owner-a", "work_task_msg", `{"text":"继续"}`)); err != nil {
		t.Fatal(err)
	}
	// 另一个 owner 用同一个键：不能复用别人的排队记录。
	if _, _, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-shared", "owner-b", "work_task_msg", `{"text":"继续"}`)); err != store.ErrConflict {
		t.Fatalf("跨 owner 同键应冲突，err=%v", err)
	}
	// 另一个 owner 的队列里不能出现这条记录。
	pending, err := repos.RemotePending.ListPending(ctx, "owner-b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("owner-b 队列里不该出现别人的指令：%+v", pending)
	}
}

func TestRemotePendingEnqueueRejectsDifferentKind(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)

	if _, _, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-kind", "owner-a", "work_task_msg", `{"text":"继续"}`)); err != nil {
		t.Fatal(err)
	}
	// 同一个 owner、同一个键，但换了操作类型：不是同一条指令。
	if _, _, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-kind", "owner-a", "work_task_action", `{"text":"继续"}`)); err != store.ErrConflict {
		t.Fatalf("跨 kind 同键应冲突，err=%v", err)
	}
}

func TestRemotePendingEnqueueRejectsDifferentPayload(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)

	if _, _, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-payload", "owner-a", "work_task_msg", `{"text":"继续"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repos.RemotePending.Enqueue(ctx, pendingItem("key-payload", "owner-a", "work_task_msg", `{"text":"换一句"}`)); err != store.ErrConflict {
		t.Fatalf("同键不同内容应冲突，err=%v", err)
	}
	// JSON 空白与键序不同视为同一条指令。
	if _, inserted, err := repos.RemotePending.Enqueue(ctx,
		pendingItem("key-payload", "owner-a", "work_task_msg", "{\n  \"text\": \"继续\"\n}")); err != nil || inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
}

func TestRemotePendingConcurrentEnqueueSameKey(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)

	// 并发入队同一个键：只能有一条记录，其余都拿到同一条。
	const workers = 8
	var wg sync.WaitGroup
	results := make([]error, workers)
	insertedCount := 0
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, inserted, err := repos.RemotePending.Enqueue(ctx,
				pendingItem("key-race", "owner-a", "work_task_msg", `{"text":"继续"}`))
			results[i] = err
			if err == nil && inserted {
				mu.Lock()
				insertedCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Fatalf("并发入队 %d 失败：%v", i, err)
		}
	}
	if insertedCount != 1 {
		t.Fatalf("并发入队插入 %d 条，应恰好一条", insertedCount)
	}
	pending, err := repos.RemotePending.ListPending(ctx, "owner-a", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}
