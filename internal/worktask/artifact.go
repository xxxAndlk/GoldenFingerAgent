package worktask

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

const maxArtifactText = 512 << 10

type ArtifactService struct {
	DB    *store.DB
	Tasks *Service
	Root  string
	Now   func() time.Time
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
		ContentText: content, ContentHash: hex.EncodeToString(hash[:]), MimeType: "text/plain",
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
	}
	if err := repos.ArtifactVersions.Add(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
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
