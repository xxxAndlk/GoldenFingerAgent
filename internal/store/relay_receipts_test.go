package store_test

import (
	"context"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func TestRelayReceiptRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := store.NewRelayReceiptRepo(db)
	now := time.Now()

	// 第一次执行：查不到回执。
	if _, _, found, err := repo.Lookup(ctx, "cmd-1"); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	// 标开始：留下 running，进程中途退出时这就是「结果未知」的证据。
	if err := repo.Begin(ctx, "cmd-1", now); err != nil {
		t.Fatal(err)
	}
	status, _, found, err := repo.Lookup(ctx, "cmd-1")
	if err != nil || !found || status != store.RelayReceiptRunning {
		t.Fatalf("status=%q found=%v err=%v", status, found, err)
	}
	// 记结果：再查就是 done，重投时按它回放。
	if err := repo.Complete(ctx, "cmd-1", []byte(`{"ok":true,"reply":"已执行"}`), now); err != nil {
		t.Fatal(err)
	}
	status, raw, found, err := repo.Lookup(ctx, "cmd-1")
	if err != nil || !found || status != store.RelayReceiptDone {
		t.Fatalf("status=%q found=%v err=%v", status, found, err)
	}
	if string(raw) != `{"ok":true,"reply":"已执行"}` {
		t.Fatalf("result=%s", raw)
	}
}

func TestRelayReceiptBeginDoesNotOverwriteDone(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := store.NewRelayReceiptRepo(db)
	now := time.Now()

	// 已经完成的指令被重投时，Begin 不能把 done 退回 running，
	// 否则重投会被误判成「上次中断」。
	if err := repo.Complete(ctx, "cmd-2", []byte(`{"ok":true}`), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Begin(ctx, "cmd-2", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, _, _, err := repo.Lookup(ctx, "cmd-2")
	if err != nil {
		t.Fatal(err)
	}
	if status != store.RelayReceiptDone {
		t.Fatalf("status=%q, done 被降级成 running", status)
	}
}

func TestRelayReceiptPruneDropsOld(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := store.NewRelayReceiptRepo(db)
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now()

	if err := repo.Complete(ctx, "old", []byte(`{}`), old); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, "recent", []byte(`{}`), recent); err != nil {
		t.Fatal(err)
	}
	if err := repo.Prune(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, found, _ := repo.Lookup(ctx, "old"); found {
		t.Fatal("过期回执没有被清理")
	}
	if _, _, found, _ := repo.Lookup(ctx, "recent"); !found {
		t.Fatal("新回执被误删")
	}
}
