package deliveryclient

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goldenfinger/agent/internal/cloudauth"
	"goldenfinger/agent/internal/deliverycloud"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

type workerFixture struct {
	worker       *Worker
	client       *Client
	cloud        *deliverycloud.Server
	ts           *httptest.Server
	localDB      *store.DB
	repos        *store.Repos
	artifacts    *worktask.ArtifactService
	ownerID      string
	taskID       string
	artifactID   string
	androidID    string
	androidToken string
	now          time.Time
	delivered    int
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	ctx := context.Background()
	localDB, err := store.Connect(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(localDB.Close)
	repos := store.NewRepos(localDB)
	user := &store.User{UserType: store.UserGeneral, Name: "测试", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	tasks := worktask.New(localDB)
	tasks.Now = func() time.Time { return now }
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: user.ID, Title: "测试", Goal: "测试", Status: store.WorkTaskDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	src := filepath.Join(workdir, "说明.txt")
	if err := os.WriteFile(src, []byte("交付内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts := worktask.NewArtifactService(localDB, tasks, filepath.Join(t.TempDir(), "artifacts"))
	artifacts.Workspace = workdir
	artifacts.Now = func() time.Time { return now }
	artifact, _, err := artifacts.CreateFileDraft(ctx, worktask.CreateFileArtifactInput{
		OwnerID: user.ID, TaskID: task.ID, Path: src, Title: "说明",
	})
	if err != nil {
		t.Fatal(err)
	}

	cloudDB, err := store.Connect(ctx, filepath.Join(t.TempDir(), "cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cloudDB.Close)
	cloudRepos := store.NewRepos(cloudDB)
	desktopToken, _, err := cloudauth.CreateDevice(ctx, cloudRepos, "u1", "desktop",
		[]string{cloudauth.ScopeDeliveryRead, cloudauth.ScopeDeliveryWrite})
	if err != nil {
		t.Fatal(err)
	}
	androidToken, android, err := cloudauth.CreateDevice(ctx, cloudRepos, "u1", "android",
		[]string{cloudauth.ScopeDeliveryRead})
	if err != nil {
		t.Fatal(err)
	}
	cloud := deliverycloud.New(deliverycloud.Config{
		DB: cloudDB, Dir: filepath.Join(t.TempDir(), "delivery"), TTL: 72 * time.Hour,
		MaxBytes: 4 << 20, Now: func() time.Time { return now },
	})
	ts := httptest.NewServer(cloud.Handler())
	t.Cleanup(ts.Close)
	client, err := New(Config{BaseURL: ts.URL, Token: desktopToken, DeviceID: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	f := &workerFixture{
		client: client, cloud: cloud, ts: ts, localDB: localDB, repos: repos,
		artifacts: artifacts, ownerID: user.ID, taskID: task.ID,
		artifactID: artifact.ID, androidID: android.ID, androidToken: androidToken, now: now,
	}
	worker := NewWorker(WorkerConfig{
		DB: localDB, Client: client, Artifacts: artifacts, OwnerID: user.ID,
		Now:    func() time.Time { return now },
		Logger: func(string, ...any) {},
		OnDelivered: func(context.Context, *store.DeliveryOutboxEntry, *Delivery) error {
			f.delivered++
			return nil
		},
	})
	f.worker = worker
	// 便于后续断言：把创建的 artifact 挂到 fixture 上。
	f.artifactID = artifact.ID
	return f
}

func TestWorkerUploadsAndEnqueues(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()
	key, err := f.worker.Enqueue(ctx, f.ownerID, f.artifactID, 0, f.androidID, false)
	if err != nil {
		t.Fatal(err)
	}
	if key == "" {
		t.Fatal("empty idempotency key")
	}
	again, err := f.worker.Enqueue(ctx, f.ownerID, f.artifactID, 0, f.androidID, false)
	if err != nil || again != key {
		t.Fatalf("idempotent enqueue key=%s err=%v", again, err)
	}
	if uploaded := f.worker.RunOnce(ctx); uploaded != 1 {
		t.Fatalf("uploaded=%d", uploaded)
	}
	entry, err := f.repos.DeliveryOutbox.Get(ctx, key)
	if err != nil || entry.Status != store.DeliveryOutboxDone || entry.DeliveryID == "" {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	if f.delivered != 1 {
		t.Fatalf("OnDelivered called %d times", f.delivered)
	}
	androidClient, err := New(Config{BaseURL: f.ts.URL, Token: f.androidToken, DeviceID: "android"})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := androidClient.Inbox(ctx, 10)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}
	if inbox[0].Name != "说明.txt" || inbox[0].SizeBytes != int64(len("交付内容")) {
		t.Fatalf("delivery=%+v", inbox[0])
	}
	// 第二次运行不应重复上传。
	if uploaded := f.worker.RunOnce(ctx); uploaded != 0 {
		t.Fatalf("second run uploaded=%d", uploaded)
	}
}

func TestWorkerForwardsExecutablePermission(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()
	src := filepath.Join(f.artifacts.Workspace, "run.bat")
	if err := os.WriteFile(src, []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifact, _, err := f.artifacts.CreateFileDraft(ctx, worktask.CreateFileArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Path: src, AllowExecutable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.worker.Enqueue(ctx, f.ownerID, artifact.ID, 0, f.androidID, true)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded := f.worker.RunOnce(ctx); uploaded != 1 {
		t.Fatalf("uploaded=%d", uploaded)
	}
	entry, err := f.repos.DeliveryOutbox.Get(ctx, key)
	if err != nil || entry.Status != store.DeliveryOutboxDone {
		t.Fatalf("entry=%+v err=%v (executable permission lost?)", entry, err)
	}
}

func TestClientDownloadAndAck(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()
	key, err := f.worker.Enqueue(ctx, f.ownerID, f.artifactID, 0, f.androidID, false)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded := f.worker.RunOnce(ctx); uploaded != 1 {
		t.Fatalf("uploaded=%d", uploaded)
	}
	entry, err := f.repos.DeliveryOutbox.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	androidClient, err := New(Config{BaseURL: f.ts.URL, Token: f.androidToken, DeviceID: "android"})
	if err != nil {
		t.Fatal(err)
	}
	download, err := androidClient.Download(ctx, entry.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(download.Body)
	download.Body.Close()
	if !bytes.Equal(raw, []byte("交付内容")) || download.SHA256 == "" {
		t.Fatalf("download body=%q sha=%q", raw, download.SHA256)
	}
	inbox, err := androidClient.Inbox(ctx, 10)
	if err != nil || len(inbox) != 1 || inbox[0].AckedAt != nil {
		t.Fatalf("inbox before ack=%+v err=%v", inbox, err)
	}
	if err := androidClient.Ack(ctx, entry.DeliveryID); err != nil {
		t.Fatal(err)
	}
	inbox, err = androidClient.Inbox(ctx, 10)
	if err != nil || len(inbox) != 1 || inbox[0].AckedAt == nil {
		t.Fatalf("inbox after ack=%+v err=%v", inbox, err)
	}
}
