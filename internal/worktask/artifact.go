package worktask

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

const (
	maxArtifactText = 512 << 10
	maxArtifactFile = 100 << 20
)

type ArtifactService struct {
	DB    *store.DB
	Tasks *Service
	Root  string
	// Workspace 用于把工具传入的相对路径解析为绝对路径。
	Workspace string
	Now       func() time.Time
}

func NewArtifactService(db *store.DB, tasks *Service, root string) *ArtifactService {
	return &ArtifactService{DB: db, Tasks: tasks, Root: root, Now: time.Now}
}

func (s *ArtifactService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type CreateArtifactInput struct {
	OwnerID    string
	TaskID     string
	Kind       string
	Title      string
	Content    string
	SourceRefs []string
	JobID      string
}

func (s *ArtifactService) CreateDraft(ctx context.Context, in CreateArtifactInput) (*store.Artifact, *store.ArtifactVersion, error) {
	if strings.TrimSpace(in.OwnerID) == "" || strings.TrimSpace(in.TaskID) == "" {
		return nil, nil, fmt.Errorf("%w: owner/task required", ErrInvalid)
	}
	if len([]rune(in.Content)) > maxArtifactText {
		return nil, nil, fmt.Errorf("%w: Artifact 内容超过上限", ErrInvalid)
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = store.ArtifactKindText
	}
	if !validArtifactKind(kind) {
		return nil, nil, fmt.Errorf("%w: artifact kind 无效", ErrInvalid)
	}
	repos := store.NewRepos(s.DB)
	if _, err := repos.WorkTasks.Get(ctx, in.OwnerID, in.TaskID); err != nil {
		return nil, nil, err
	}
	now := s.now()
	artifact := &store.Artifact{
		OwnerUserID: in.OwnerID, TaskID: in.TaskID, Kind: kind,
		Title:          firstNonEmpty(strings.TrimSpace(in.Title), "交付物"),
		CreatedByJobID: in.JobID,
	}
	artifact.ID = uuid.NewString()
	artifact.StorageDir = filepath.Join(artifact.TaskID, artifact.ID)
	var version *store.ArtifactVersion
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		txRepos := store.NewRepos(tx)
		if err := txRepos.Artifacts.Create(ctx, artifact); err != nil {
			return err
		}
		v, err := s.writeVersion(ctx, txRepos, artifact, 1, in.Content, "", in.SourceRefs, in.JobID, now)
		if err != nil {
			return err
		}
		version = v
		changed, err := txRepos.Artifacts.Advance(ctx, in.OwnerID, artifact.ID, 0, 1, store.ArtifactDraft, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: artifact 版本冲突", ErrConflict)
		}
		artifact.CurrentVersion = 1
		artifact.Status = store.ArtifactDraft
		if _, err := txRepos.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: in.OwnerID, Kind: "artifact.created", EntityType: "artifact",
			EntityID: artifact.ID, Payload: mustJSON(map[string]any{
				"task_id": in.TaskID, "version": 1,
			}),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return artifact, version, nil
}

func (s *ArtifactService) AddVersion(ctx context.Context, ownerID, artifactID string, expectedVersion int,
	content, changeSummary string, sourceRefs []string, jobID string) (*store.Artifact, *store.ArtifactVersion, error) {
	if len([]rune(content)) > maxArtifactText {
		return nil, nil, fmt.Errorf("%w: Artifact 内容超过上限", ErrInvalid)
	}
	now := s.now()
	var artifact *store.Artifact
	var version *store.ArtifactVersion
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		current, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && current.CurrentVersion != expectedVersion {
			return fmt.Errorf("%w: artifact version %d != %d", ErrConflict, current.CurrentVersion, expectedVersion)
		}
		next := current.CurrentVersion + 1
		v, err := s.writeVersion(ctx, repos, current, next, content, changeSummary, sourceRefs, jobID, now)
		if err != nil {
			return err
		}
		changed, err := repos.Artifacts.Advance(ctx, ownerID, artifactID, current.CurrentVersion, next, store.ArtifactDraft, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: artifact 版本冲突", ErrConflict)
		}
		current.CurrentVersion = next
		current.Status = store.ArtifactDraft
		artifact, version = current, v
		_, err = repos.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: ownerID, Kind: "artifact.version", EntityType: "artifact",
			EntityID: artifactID, Payload: mustJSON(map[string]any{
				"task_id": current.TaskID, "version": next,
			}),
		})
		return err
	})
	return artifact, version, err
}

func (s *ArtifactService) writeVersion(ctx context.Context, repos *store.Repos, artifact *store.Artifact,
	version int, content, changeSummary string, sourceRefs []string, jobID string, now time.Time) (*store.ArtifactVersion, error) {
	content = strings.TrimSpace(content)
	hash := sha256.Sum256([]byte(content))
	refs, err := json.Marshal(sourceRefs)
	if err != nil {
		return nil, err
	}
	v := &store.ArtifactVersion{
		OwnerUserID: artifact.OwnerUserID, ArtifactID: artifact.ID, Version: version,
		StorageKind: store.ArtifactStorageText, ContentText: content,
		ContentHash: hex.EncodeToString(hash[:]), MimeType: "text/plain",
		SizeBytes: int64(len(content)), SourceRefs: refs, ChangeSummary: changeSummary,
		CreatedByJobID: jobID, CreatedAt: now,
	}
	if s.Root != "" {
		dir := filepath.Join(s.Root, safeSegment(artifact.TaskID), safeSegment(artifact.ID))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("v%d.txt", version)
		if artifact.Kind == store.ArtifactKindCode {
			name = fmt.Sprintf("v%d.code", version)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return nil, err
		}
		v.RelativePath = filepath.ToSlash(filepath.Join(safeSegment(artifact.TaskID), safeSegment(artifact.ID), name))
		v.OriginalName = name
	}
	if err := repos.ArtifactVersions.Add(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}

// CreateFileArtifactInput 从本地已授权文件创建不可变 Artifact 版本。
type CreateFileArtifactInput struct {
	OwnerID         string
	TaskID          string
	Kind            string
	Title           string
	Path            string
	ChangeSummary   string
	SourceRefs      []string
	JobID           string
	AllowExecutable bool
}

func (s *ArtifactService) CreateFileDraft(ctx context.Context, in CreateFileArtifactInput) (*store.Artifact, *store.ArtifactVersion, error) {
	if strings.TrimSpace(in.OwnerID) == "" || strings.TrimSpace(in.TaskID) == "" {
		return nil, nil, fmt.Errorf("%w: owner/task required", ErrInvalid)
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, nil, fmt.Errorf("%w: 文件路径必填", ErrInvalid)
	}
	if s.Root == "" {
		return nil, nil, fmt.Errorf("%w: Artifact 根目录未配置", ErrInvalid)
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = store.ArtifactKindFile
	}
	if !validArtifactKind(kind) {
		return nil, nil, fmt.Errorf("%w: artifact kind 无效", ErrInvalid)
	}
	repos := store.NewRepos(s.DB)
	if _, err := repos.WorkTasks.Get(ctx, in.OwnerID, in.TaskID); err != nil {
		return nil, nil, err
	}
	now := s.now()
	artifact := &store.Artifact{
		OwnerUserID: in.OwnerID, TaskID: in.TaskID, Kind: kind,
		Title:          firstNonEmpty(strings.TrimSpace(in.Title), filepath.Base(in.Path)),
		CreatedByJobID: in.JobID,
	}
	artifact.ID = uuid.NewString()
	artifact.StorageDir = filepath.Join(artifact.TaskID, artifact.ID)
	var version *store.ArtifactVersion
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		txr := store.NewRepos(tx)
		if err := txr.Artifacts.Create(ctx, artifact); err != nil {
			return err
		}
		v, err := s.writeFileVersion(ctx, txr, artifact, 1, in.Path, in.ChangeSummary,
			in.SourceRefs, in.JobID, in.AllowExecutable, now)
		if err != nil {
			return err
		}
		version = v
		changed, err := txr.Artifacts.Advance(ctx, in.OwnerID, artifact.ID, 0, 1, store.ArtifactDraft, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: artifact 版本冲突", ErrConflict)
		}
		artifact.CurrentVersion = 1
		artifact.Status = store.ArtifactDraft
		_, err = txr.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: in.OwnerID, Kind: "artifact.created", EntityType: "artifact",
			EntityID: artifact.ID, Payload: mustJSON(map[string]any{
				"task_id": in.TaskID, "version": 1, "storage_kind": store.ArtifactStorageFile,
			}),
		})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return artifact, version, nil
}

// AddFileVersion 为已有 Artifact 追加一个文件快照版本。
func (s *ArtifactService) AddFileVersion(ctx context.Context, ownerID, artifactID string, expectedVersion int,
	path, changeSummary string, sourceRefs []string, jobID string, allowExecutable bool) (*store.Artifact, *store.ArtifactVersion, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("%w: 文件路径必填", ErrInvalid)
	}
	now := s.now()
	var artifact *store.Artifact
	var version *store.ArtifactVersion
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		current, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && current.CurrentVersion != expectedVersion {
			return fmt.Errorf("%w: artifact version %d != %d", ErrConflict, current.CurrentVersion, expectedVersion)
		}
		next := current.CurrentVersion + 1
		v, err := s.writeFileVersion(ctx, repos, current, next, path, changeSummary,
			sourceRefs, jobID, allowExecutable, now)
		if err != nil {
			return err
		}
		changed, err := repos.Artifacts.Advance(ctx, ownerID, artifactID, current.CurrentVersion, next, store.ArtifactDraft, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: artifact 版本冲突", ErrConflict)
		}
		current.CurrentVersion = next
		current.Status = store.ArtifactDraft
		artifact, version = current, v
		return nil
	})
	return artifact, version, err
}

type snapshotInfo struct {
	RelativePath string
	OriginalName string
	MimeType     string
	Hash         string
	SourceRef    string
	Size         int64
}

func (s *ArtifactService) writeFileVersion(ctx context.Context, repos *store.Repos, artifact *store.Artifact,
	version int, sourcePath, changeSummary string, sourceRefs []string, jobID string,
	allowExecutable bool, now time.Time) (*store.ArtifactVersion, error) {
	info, err := s.snapshotFile(artifact, version, sourcePath, allowExecutable)
	if err != nil {
		return nil, err
	}
	refs, err := json.Marshal(sourceRefs)
	if err != nil {
		return nil, err
	}
	v := &store.ArtifactVersion{
		OwnerUserID: artifact.OwnerUserID, ArtifactID: artifact.ID, Version: version,
		StorageKind: store.ArtifactStorageFile, RelativePath: info.RelativePath,
		OriginalName: info.OriginalName, ContentHash: info.Hash, MimeType: info.MimeType,
		SizeBytes: info.Size, SourceRefs: refs, SourceKind: "local_path",
		SourceRef: info.SourceRef, ChangeSummary: changeSummary,
		CreatedByJobID: jobID, CreatedAt: now,
	}
	if err := repos.ArtifactVersions.Add(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}

// snapshotFile 校验源文件并复制为不可变快照：先写临时文件、边写边算哈希，
// 校验大小后原子改名。失败只可能留下孤儿文件，不会出现可下载的半成品。
func (s *ArtifactService) snapshotFile(artifact *store.Artifact, version int, sourcePath string, allowExecutable bool) (snapshotInfo, error) {
	var info snapshotInfo
	abs := strings.TrimSpace(sourcePath)
	if !filepath.IsAbs(abs) {
		base := s.Workspace
		if base == "" {
			base, _ = os.Getwd()
		}
		abs = filepath.Join(base, abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return info, fmt.Errorf("%w: 源文件不存在或不可访问", ErrInvalid)
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return info, fmt.Errorf("%w: 源文件不可读", ErrInvalid)
	}
	if !st.Mode().IsRegular() {
		return info, fmt.Errorf("%w: 只支持普通文件", ErrInvalid)
	}
	if st.Size() > maxArtifactFile {
		return info, fmt.Errorf("%w: 文件超过 %d MiB 上限", ErrInvalid, maxArtifactFile>>20)
	}
	original := filepath.Base(resolved)
	if !allowExecutable && isExecutableName(original) {
		return info, fmt.Errorf("%w: 可执行文件/脚本默认拒绝，需显式允许", ErrInvalid)
	}
	ext := normalizedExt(original)
	dir := filepath.Join(s.Root, safeSegment(artifact.TaskID), safeSegment(artifact.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return info, err
	}
	tmp, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return info, err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		_ = os.Remove(tmpName)
	}
	src, err := os.Open(resolved)
	if err != nil {
		cleanup()
		return info, fmt.Errorf("%w: 源文件不可读", ErrInvalid)
	}
	defer src.Close()
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(src, maxArtifactFile+1))
	if err != nil {
		cleanup()
		return info, err
	}
	if n > maxArtifactFile {
		cleanup()
		return info, fmt.Errorf("%w: 文件超过 %d MiB 上限", ErrInvalid, maxArtifactFile>>20)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return info, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return info, err
	}
	finalName := fmt.Sprintf("v%d%s", version, ext)
	finalPath := filepath.Join(dir, finalName)
	if err := os.Rename(tmpName, finalPath); err != nil {
		_ = os.Remove(tmpName)
		return info, err
	}
	rel, err := filepath.Rel(s.Root, finalPath)
	if err != nil {
		return info, err
	}
	info = snapshotInfo{
		RelativePath: filepath.ToSlash(rel), OriginalName: original,
		MimeType: detectMimeType(finalPath, ext), Hash: hex.EncodeToString(hasher.Sum(nil)),
		SourceRef: resolved, Size: n,
	}
	return info, nil
}

// OpenVersion 打开某个版本的内容；文件版本从 Artifact 根目录读取，文本版本从 DB 读取。
func (s *ArtifactService) OpenVersion(ctx context.Context, ownerID, artifactID string, version int) (io.ReadSeekCloser, *store.ArtifactVersion, error) {
	v, err := s.GetVersion(ctx, ownerID, artifactID, version)
	if err != nil {
		return nil, nil, err
	}
	if v.StorageKind != store.ArtifactStorageFile {
		return seekCloser{bytes.NewReader([]byte(v.ContentText))}, v, nil
	}
	if s.Root == "" || v.RelativePath == "" {
		return nil, v, fmt.Errorf("%w: 文件快照不存在", ErrInvalid)
	}
	rootAbs, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, v, err
	}
	full, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(v.RelativePath)))
	if err != nil {
		return nil, v, err
	}
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, v, fmt.Errorf("%w: 路径越界", ErrInvalid)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, v, fmt.Errorf("%w: 文件快照丢失", store.ErrNotFound)
	}
	return f, v, nil
}

type seekCloser struct{ *bytes.Reader }

func (seekCloser) Close() error { return nil }

func normalizedExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) > 12 || len(ext) < 2 {
		return ""
	}
	for _, r := range ext {
		if r == '.' {
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

var executableExts = map[string]bool{
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".ps1": true, ".psm1": true,
	".vbs": true, ".vbe": true, ".wsf": true, ".wsh": true, ".scr": true, ".msi": true,
	".msp": true, ".lnk": true, ".reg": true, ".apk": true, ".app": true, ".dmg": true,
	".pkg": true, ".deb": true, ".rpm": true, ".jar": true, ".sh": true, ".bash": true,
	".zsh": true, ".py": true, ".rb": true, ".pl": true, ".js": true, ".jse": true,
}

func isExecutableName(name string) bool {
	return executableExts[strings.ToLower(filepath.Ext(name))]
}

func detectMimeType(path, ext string) string {
	if ext != "" {
		if value := mime.TypeByExtension(ext); value != "" {
			return value
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return http.DetectContentType(buf[:n])
}

func (s *ArtifactService) Get(ctx context.Context, ownerID, artifactID string) (*store.Artifact, []store.ArtifactVersion, error) {
	repos := store.NewRepos(s.DB)
	artifact, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
	if err != nil {
		return nil, nil, err
	}
	versions, err := repos.ArtifactVersions.List(ctx, ownerID, artifactID)
	return artifact, versions, err
}

func (s *ArtifactService) GetVersion(ctx context.Context, ownerID, artifactID string, version int) (*store.ArtifactVersion, error) {
	return store.NewRepos(s.DB).ArtifactVersions.Get(ctx, ownerID, artifactID, version)
}

func (s *ArtifactService) SubmitForAcceptance(ctx context.Context, ownerID, artifactID string, expectedVersion int) (*store.Artifact, error) {
	now := s.now()
	var artifact *store.Artifact
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		current, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && current.CurrentVersion != expectedVersion {
			return fmt.Errorf("%w: artifact version %d != %d", ErrConflict, current.CurrentVersion, expectedVersion)
		}
		changed, err := repos.Artifacts.SetStatus(ctx, ownerID, artifactID, current.CurrentVersion, store.ArtifactPending, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: Artifact 已变化", ErrConflict)
		}
		current.Status = store.ArtifactPending
		artifact = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.Tasks != nil {
		detail, err := s.Tasks.Get(ctx, ownerID, artifact.TaskID)
		if err == nil && detail.Task.Status != store.WorkTaskPendingAccept {
			if _, err := s.Tasks.PendingAcceptance(ctx, ownerID, artifact.TaskID,
				detail.Task.CurrentJobID, detail.Task.Version, firstNonEmpty(detail.Task.Title, "等待验收")); err != nil &&
				!errors.Is(err, ErrConflict) {
				return artifact, err
			}
		}
	}
	return artifact, nil
}

func (s *ArtifactService) Accept(ctx context.Context, ownerID, artifactID string, expectedVersion int) (*store.Artifact, error) {
	now := s.now()
	var artifact *store.Artifact
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		current, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && current.CurrentVersion != expectedVersion {
			return fmt.Errorf("%w: artifact version %d != %d", ErrConflict, current.CurrentVersion, expectedVersion)
		}
		changed, err := repos.Artifacts.SetStatus(ctx, ownerID, artifactID, current.CurrentVersion, store.ArtifactAccepted, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: Artifact 已变化", ErrConflict)
		}
		current.Status = store.ArtifactAccepted
		artifact = current
		_, _ = repos.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: ownerID, Kind: "artifact.accepted", EntityType: "artifact",
			EntityID: artifactID, Payload: mustJSON(map[string]any{
				"task_id": current.TaskID, "version": current.CurrentVersion,
			}),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.Tasks != nil {
		detail, err := s.Tasks.Get(ctx, ownerID, artifact.TaskID)
		if err == nil {
			all := true
			for _, item := range detail.Artifacts {
				if item.ID == artifact.ID {
					item.Status = store.ArtifactAccepted
				}
				if item.Status == store.ArtifactPending || item.Status == store.ArtifactDraft {
					all = false
					break
				}
			}
			if all && detail.Task.Status == store.WorkTaskPendingAccept {
				_, _ = s.Tasks.Complete(ctx, ownerID, artifact.TaskID, detail.Task.Version)
			}
		}
	}
	return artifact, nil
}

func (s *ArtifactService) Reject(ctx context.Context, ownerID, artifactID string, expectedVersion int, reason string) (*store.Artifact, error) {
	now := s.now()
	var artifact *store.Artifact
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		current, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && current.CurrentVersion != expectedVersion {
			return fmt.Errorf("%w: artifact version %d != %d", ErrConflict, current.CurrentVersion, expectedVersion)
		}
		changed, err := repos.Artifacts.SetStatus(ctx, ownerID, artifactID, current.CurrentVersion, store.ArtifactRejected, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: Artifact 已变化", ErrConflict)
		}
		current.Status = store.ArtifactRejected
		artifact = current
		_, _ = repos.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: ownerID, Kind: "artifact.rejected", EntityType: "artifact",
			EntityID: artifactID, Payload: mustJSON(map[string]any{
				"task_id": current.TaskID, "version": current.CurrentVersion, "reason": reason,
			}),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.Tasks != nil {
		if detail, err := s.Tasks.Get(ctx, ownerID, artifact.TaskID); err == nil && detail.Task.Status == store.WorkTaskPendingAccept {
			_, _ = s.Tasks.Reopen(ctx, ownerID, artifact.TaskID, detail.Task.Version)
			_, _ = s.Tasks.AddMessage(ctx, ownerID, artifact.TaskID,
				"请按验收意见修改："+firstNonEmpty(strings.TrimSpace(reason), "未通过验收"), nil, "")
		}
	}
	return artifact, nil
}

func (s *ArtifactService) MarkDelivered(ctx context.Context, ownerID, artifactID string, expectedVersion int) (*store.Artifact, error) {
	now := s.now()
	var artifact *store.Artifact
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		current, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
		if err != nil {
			return err
		}
		if expectedVersion >= 0 && current.CurrentVersion != expectedVersion {
			return fmt.Errorf("%w: artifact version %d != %d", ErrConflict, current.CurrentVersion, expectedVersion)
		}
		changed, err := repos.Artifacts.SetStatus(ctx, ownerID, artifactID, current.CurrentVersion, store.ArtifactDelivered, now)
		if err != nil || !changed {
			return fmt.Errorf("%w: Artifact 已变化", ErrConflict)
		}
		current.Status = store.ArtifactDelivered
		artifact = current
		return nil
	})
	return artifact, err
}

// Diff 返回文本版本的 unified diff；二进制/文件只返回版本元信息。
func (s *ArtifactService) Diff(ctx context.Context, ownerID, artifactID string, from, to int) (string, error) {
	repos := store.NewRepos(s.DB)
	artifact, err := repos.Artifacts.Get(ctx, ownerID, artifactID)
	if err != nil {
		return "", err
	}
	if artifact.Kind == store.ArtifactKindFile {
		return "", fmt.Errorf("%w: 二进制文件只提供版本元信息", ErrInvalid)
	}
	a, err := repos.ArtifactVersions.Get(ctx, ownerID, artifactID, from)
	if err != nil {
		return "", err
	}
	b, err := repos.ArtifactVersions.Get(ctx, ownerID, artifactID, to)
	if err != nil {
		return "", err
	}
	if a.StorageKind != store.ArtifactStorageText || b.StorageKind != store.ArtifactStorageText {
		return "", fmt.Errorf("%w: 文件版本只提供元信息", ErrInvalid)
	}
	return unifiedDiff(a.ContentText, b.ContentText, fmt.Sprintf("v%d", from), fmt.Sprintf("v%d", to)), nil
}

func unifiedDiff(oldText, newText, oldLabel, newLabel string) string {
	oldLines := strings.Split(strings.ReplaceAll(oldText, "\r\n", "\n"), "\n")
	newLines := strings.Split(strings.ReplaceAll(newText, "\r\n", "\n"), "\n")
	const maxLines = 2000
	if len(oldLines) > maxLines || len(newLines) > maxLines {
		return fmt.Sprintf("--- %s\n+++ %s\n@@ 差异过大，仅返回摘要 @@\n- 旧文本 %d 行\n+ 新文本 %d 行\n",
			oldLabel, newLabel, len(oldLines), len(newLines))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldLabel, newLabel)
	if len(oldLines) == len(newLines) {
		for i := range oldLines {
			if oldLines[i] != newLines[i] {
				fmt.Fprintf(&b, "@@ line %d @@\n-%s\n+%s\n", i+1, oldLines[i], newLines[i])
			}
		}
		return b.String()
	}
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
	for _, line := range oldLines {
		fmt.Fprintf(&b, "-%s\n", line)
	}
	for _, line := range newLines {
		fmt.Fprintf(&b, "+%s\n", line)
	}
	return b.String()
}

func validArtifactKind(kind string) bool {
	switch kind {
	case store.ArtifactKindText, store.ArtifactKindDoc, store.ArtifactKindCode,
		store.ArtifactKindAnalysis, store.ArtifactKindEmail, store.ArtifactKindFile,
		store.ArtifactKindExternal:
		return true
	}
	return false
}

func safeSegment(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "x"
	}
	return out
}
