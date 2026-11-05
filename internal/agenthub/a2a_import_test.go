package agenthub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

type importFixture struct {
	db        *store.DB
	repos     *store.Repos
	user      *store.User
	task      *store.WorkTask
	artifacts *worktask.ArtifactService
	tasks     *worktask.Service
	manager   *Manager
	workspace string
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	repos := store.NewRepos(db)
	user := &store.User{UserType: store.UserGeneral, Name: "测试", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	tasks := worktask.New(db)
	tasks.Now = func() time.Time { return now }
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: user.ID, Title: "接收外部文件", Goal: "接收外部文件", Status: store.WorkTaskDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	svc := worktask.NewArtifactService(db, tasks, filepath.Join(t.TempDir(), "artifacts"))
	svc.Workspace = workspace
	svc.Now = func() time.Time { return now }
	manager := New(workspace, filepath.Join(t.TempDir(), "agents.json"))
	return &importFixture{
		db: db, repos: repos, user: user, task: task, artifacts: svc,
		tasks: tasks, manager: manager, workspace: workspace,
	}
}

func (f *importFixture) file(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(f.workspace, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportArtifactsIntoWorkTask(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	path := f.file(t, "报告.txt", "外部 Agent 产物")
	imported, err := f.manager.ImportArtifacts(ctx, f.artifacts, f.user.ID, f.task.ID, "", "agent-x",
		[]AgentArtifact{{ID: "a1", Name: "报告.txt", LocalPath: path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].ArtifactID == "" {
		t.Fatalf("imported=%+v", imported)
	}
	artifact, versions, err := f.artifacts.Get(ctx, f.user.ID, imported[0].ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.TaskID != f.task.ID || len(versions) != 1 || versions[0].StorageKind != store.ArtifactStorageFile {
		t.Fatalf("artifact=%+v versions=%+v", artifact, versions)
	}
}

func TestImportArtifactsNeverDownloadsURLWithoutDedicatedTool(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	imported, err := f.manager.ImportArtifacts(ctx, f.artifacts, f.user.ID, f.task.ID, "", "agent-x",
		[]AgentArtifact{{ID: "a2", Name: "f.bin", ExternalURI: "http://127.0.0.1/f"}})
	if len(imported) != 0 || err == nil || errors.Is(err, ErrUnsafeArtifactURL) ||
		!strings.Contains(err.Error(), "download_agent_artifact 独立授权") {
		t.Fatalf("imported=%+v err=%v", imported, err)
	}
	if list, _ := f.repos.Artifacts.ListByTask(ctx, f.user.ID, f.task.ID); len(list) != 0 {
		t.Fatalf("unauthorized URL must not create artifacts: %+v", list)
	}
}

func TestAttachArtifactsUsesCurrentWorkTask(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	path := f.file(t, "a.txt", "hello")
	runtime := &agent.Runtime{
		WorkTaskID: f.task.ID,
		Tools:      &agent.ToolServices{Artifacts: f.artifacts},
	}
	tc := &agent.ToolContext{Session: &agent.Session{UserID: f.user.ID}, Runtime: runtime}
	inv := &Invocation{
		AgentID: "agent-y", Output: "done",
		Artifacts: []AgentArtifact{{ID: "a3", Name: "a.txt", LocalPath: path}},
	}
	f.manager.attachArtifacts(ctx, tc, inv)
	if !strings.Contains(inv.Output, "已转为 WorkTask 交付物") {
		t.Fatalf("output=%q", inv.Output)
	}
	list, err := f.repos.Artifacts.ListByTask(ctx, f.user.ID, f.task.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("artifacts=%+v err=%v", list, err)
	}
}

func TestAsyncDispatcherImportsArtifacts(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	dispatcher := NewDispatcher(f.db, f.manager)
	dispatcher.Artifacts = f.artifacts
	dispatcher.Tasks = f.tasks
	path := f.file(t, "async.txt", "async file")
	job := &store.AgentJob{ID: "job-import", OwnerUserID: f.user.ID, AgentID: "agent-z"}
	note, err := dispatcher.importArtifacts(ctx, job, []AgentArtifact{{ID: "a4", Name: "async.txt", LocalPath: path}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "已把 1 个文件产物转为交付物") {
		t.Fatalf("note=%q", note)
	}
	tasks, err := f.tasks.List(ctx, f.user.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range tasks {
		list, _ := f.repos.Artifacts.ListByTask(ctx, f.user.ID, task.ID)
		if len(list) == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("async artifact not attached to any WorkTask: tasks=%+v", tasks)
	}
}
