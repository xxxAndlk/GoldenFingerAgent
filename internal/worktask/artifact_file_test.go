package worktask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

type artifactFixture struct {
	svc     *ArtifactService
	tasks   *Service
	repos   *store.Repos
	root    string
	workdir string
	taskID  string
	ownerID string
}

func newArtifactFixture(t *testing.T) *artifactFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, filepath.Join(t.TempDir(), "art.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	repos := store.NewRepos(db)
	user := &store.User{UserType: store.UserGeneral, Name: "测试用户", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	tasks := New(db)
	tasks.Now = func() time.Time { return now }
	task, err := tasks.Create(ctx, CreateInput{
		OwnerID: user.ID, Title: "整理文件", Goal: "整理文件", Status: store.WorkTaskDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "artifacts")
	workdir := t.TempDir()
	svc := NewArtifactService(db, tasks, root)
	svc.Workspace = workdir
	svc.Now = func() time.Time { return now }
	return &artifactFixture{
		svc: svc, tasks: tasks, repos: repos, root: root, workdir: workdir,
		taskID: task.ID, ownerID: user.ID,
	}
}

func (f *artifactFixture) source(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(f.workdir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCreateFileDraftSnapshotsImmutably(t *testing.T) {
	f := newArtifactFixture(t)
	ctx := context.Background()
	src := f.source(t, "说明.txt", "第一版内容")
	artifact, version, err := f.svc.CreateFileDraft(ctx, CreateFileArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Path: src, Title: "项目说明",
	})
	if err != nil {
		t.Fatal(err)
	}
	if version.StorageKind != store.ArtifactStorageFile {
		t.Fatalf("storage kind=%q", version.StorageKind)
	}
	if version.OriginalName != "说明.txt" || version.SizeBytes != int64(len("第一版内容")) {
		t.Fatalf("version=%+v", version)
	}
	sum := sha256.Sum256([]byte("第一版内容"))
	if version.ContentHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash=%s", version.ContentHash)
	}
	if artifact.Kind != store.ArtifactKindFile || artifact.CurrentVersion != 1 {
		t.Fatalf("artifact=%+v", artifact)
	}
	snapshot := filepath.Join(f.root, filepath.FromSlash(version.RelativePath))
	raw, err := os.ReadFile(snapshot)
	if err != nil || string(raw) != "第一版内容" {
		t.Fatalf("snapshot=%q err=%v", raw, err)
	}

	// 源文件变化不影响已生成版本。
	if err := os.WriteFile(src, []byte("第二版内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, _, err := f.svc.OpenVersion(ctx, f.ownerID, artifact.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, _ := io.ReadAll(reader)
	if string(got) != "第一版内容" {
		t.Fatalf("open version=%q", got)
	}
}

func TestAddFileVersionKeepsPrevious(t *testing.T) {
	f := newArtifactFixture(t)
	ctx := context.Background()
	src := f.source(t, "report.md", "v1")
	artifact, _, err := f.svc.CreateFileDraft(ctx, CreateFileArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Path: src,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, version, err := f.svc.AddFileVersion(ctx, f.ownerID, artifact.ID, 1, src, "第二版", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentVersion != 2 || version.Version != 2 {
		t.Fatalf("artifact=%+v version=%+v", updated, version)
	}
	first, _, err := f.svc.OpenVersion(ctx, f.ownerID, artifact.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	raw, _ := io.ReadAll(first)
	if string(raw) != "v1" {
		t.Fatalf("v1=%q", raw)
	}
}

func TestExecutableFileRejectedByDefault(t *testing.T) {
	f := newArtifactFixture(t)
	ctx := context.Background()
	src := f.source(t, "run.bat", "@echo off")
	if _, _, err := f.svc.CreateFileDraft(ctx, CreateFileArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Path: src,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v, want ErrInvalid", err)
	}
	if _, _, err := f.svc.CreateFileDraft(ctx, CreateFileArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Path: src, AllowExecutable: true,
	}); err != nil {
		t.Fatalf("explicit allow should succeed: %v", err)
	}
}

func TestOpenVersionRejectsPathEscape(t *testing.T) {
	f := newArtifactFixture(t)
	ctx := context.Background()
	artifact, version, err := f.svc.CreateDraft(ctx, CreateArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Content: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(f.root), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.DB.ExecContext(ctx,
		`UPDATE artifact_version SET storage_kind = ?, relative_path = ? WHERE id = ?`,
		store.ArtifactStorageFile, "../secret.txt", version.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.OpenVersion(ctx, f.ownerID, artifact.ID, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v, want ErrInvalid", err)
	}
}

func TestOpenVersionMissingSnapshot(t *testing.T) {
	f := newArtifactFixture(t)
	ctx := context.Background()
	src := f.source(t, "data.bin", "binary-ish")
	artifact, version, err := f.svc.CreateFileDraft(ctx, CreateFileArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Path: src,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(version.RelativePath))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.OpenVersion(ctx, f.ownerID, artifact.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

func TestTextArtifactStillWorks(t *testing.T) {
	f := newArtifactFixture(t)
	ctx := context.Background()
	artifact, version, err := f.svc.CreateDraft(ctx, CreateArtifactInput{
		OwnerID: f.ownerID, TaskID: f.taskID, Kind: store.ArtifactKindText, Content: "纯文本",
	})
	if err != nil {
		t.Fatal(err)
	}
	if version.StorageKind != store.ArtifactStorageText {
		t.Fatalf("storage kind=%q", version.StorageKind)
	}
	reader, _, err := f.svc.OpenVersion(ctx, f.ownerID, artifact.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	raw, _ := io.ReadAll(reader)
	if string(raw) != "纯文本" {
		t.Fatalf("text=%q", raw)
	}
}
