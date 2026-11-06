package store_test

import (
	"context"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func TestMemoryOutboxDeleteOpAndRetry(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	owner := "owner-outbox"

	// 写入一条待发片段。
	if err := repos.MemoryOutbox.Enqueue(ctx, &store.MemoryOutboxEntry{
		OwnerUserID: owner, SourceID: "brief:2026-11-05:abc",
		PayloadJSON: `{"text":"x"}`, ContentHash: "h1",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := repos.MemoryOutbox.Due(ctx, time.Now(), 10)
	if err != nil || len(entries) != 1 || entries[0].Op != store.MemoryOutboxUpsert {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}

	// 删除也要排队：断网时删除不能丢。
	if err := repos.MemoryOutbox.EnqueueDelete(ctx, &store.MemoryOutboxEntry{
		OwnerUserID: owner, SourceID: "forget:brief:2026-11-05:",
		PayloadJSON: `{"source_prefix":"brief:2026-11-05:"}`, ContentHash: "h2",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err = repos.MemoryOutbox.Due(ctx, time.Now(), 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	var sawDelete bool
	for _, entry := range entries {
		if entry.Op == store.MemoryOutboxDelete {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("删除指令没有排队：%+v", entries)
	}

	// 失败条目可以重新排队（界面上的「重试」）。
	if err := repos.MemoryOutbox.MarkFailed(ctx, owner, "brief:2026-11-05:abc", "网络不通"); err != nil {
		t.Fatal(err)
	}
	pending, failed, _, err := repos.MemoryOutbox.Counts(ctx, owner)
	if err != nil || failed != 1 {
		t.Fatalf("pending=%d failed=%d err=%v", pending, failed, err)
	}
	requeued, err := repos.MemoryOutbox.RetryFailed(ctx, owner)
	if err != nil || requeued != 1 {
		t.Fatalf("requeued=%d err=%v", requeued, err)
	}
	pending, failed, _, err = repos.MemoryOutbox.Counts(ctx, owner)
	if err != nil || failed != 0 || pending != 2 {
		t.Fatalf("pending=%d failed=%d err=%v", pending, failed, err)
	}
}

func TestMemoryOutboxPurgeAllowsRewriteAfterForget(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	owner := "owner-purge"

	if err := repos.MemoryOutbox.Enqueue(ctx, &store.MemoryOutboxEntry{
		OwnerUserID: owner, SourceID: "person_notes:张阿姨:abc",
		PayloadJSON: `{"text":"x"}`, ContentHash: "h1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.MemoryOutbox.MarkSent(ctx, owner, "person_notes:张阿姨:abc", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 遗忘要清掉已发送记录：来源 ID 按内容寻址，
	// 同样的内容之后要能重新排队。
	if err := repos.MemoryOutbox.PurgeBySourcePrefix(ctx, owner, "person_notes:张阿姨:"); err != nil {
		t.Fatal(err)
	}
	pending, _, _, err := repos.MemoryOutbox.Counts(ctx, owner)
	if err != nil || pending != 0 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	if err := repos.MemoryOutbox.Enqueue(ctx, &store.MemoryOutboxEntry{
		OwnerUserID: owner, SourceID: "person_notes:张阿姨:abc",
		PayloadJSON: `{"text":"x"}`, ContentHash: "h1",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := repos.MemoryOutbox.Due(ctx, time.Now(), 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}
