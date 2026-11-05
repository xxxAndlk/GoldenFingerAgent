package deliveryclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// ArtifactReader 是 Worker 读取 Artifact 快照所需的能力（*worktask.ArtifactService 满足）。
type ArtifactReader interface {
	Get(ctx context.Context, ownerID, artifactID string) (*store.Artifact, []store.ArtifactVersion, error)
	GetVersion(ctx context.Context, ownerID, artifactID string, version int) (*store.ArtifactVersion, error)
	OpenVersion(ctx context.Context, ownerID, artifactID string, version int) (io.ReadSeekCloser, *store.ArtifactVersion, error)
}

type WorkerConfig struct {
	DB        *store.DB
	Client    *Client
	Artifacts ArtifactReader
	OwnerID   string
	Now       func() time.Time
	Logger    func(format string, args ...any)
	// OnDelivered 在云端上传成功后调用（用于写交付摘要记忆）。
	OnDelivered func(ctx context.Context, entry *store.DeliveryOutboxEntry, delivery *Delivery) error
}

// Worker 把持久 Outbox 中的交付任务上传到云端；网络中断或重启后继续。
type Worker struct {
	cfg    WorkerConfig
	notify chan struct{}
}

func NewWorker(cfg WorkerConfig) *Worker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Printf
	}
	return &Worker{cfg: cfg, notify: make(chan struct{}, 1)}
}

func (w *Worker) wake() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *Worker) Start(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.notify:
		}
	}
}

// Enqueue 校验 Artifact 版本并写入持久交付 Outbox，返回幂等键。
// 同一 Artifact 版本 + 目标设备始终映射到同一个键，重复请求不会产生重复文件。
func (w *Worker) Enqueue(ctx context.Context, ownerID, artifactID string, version int,
	targetDeviceID string, allowExecutable bool) (string, error) {
	if w == nil || w.cfg.DB == nil || w.cfg.Artifacts == nil {
		return "", errors.New("delivery: 云端交付未启用")
	}
	ownerID = strings.TrimSpace(ownerID)
	artifactID = strings.TrimSpace(artifactID)
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	if ownerID == "" || artifactID == "" || targetDeviceID == "" {
		return "", errors.New("delivery: owner/artifact/target 必填")
	}
	artifact, _, err := w.cfg.Artifacts.Get(ctx, ownerID, artifactID)
	if err != nil {
		return "", err
	}
	if version <= 0 {
		version = artifact.CurrentVersion
	}
	if _, err := w.cfg.Artifacts.GetVersion(ctx, ownerID, artifactID, version); err != nil {
		return "", err
	}
	key := idempotencyKey(ownerID, artifactID, version, targetDeviceID)
	entry := &store.DeliveryOutboxEntry{
		IdempotencyKey: key, OwnerUserID: ownerID, ArtifactID: artifactID,
		ArtifactVersion: version, TargetDeviceID: targetDeviceID,
		AllowExecutable: allowExecutable,
	}
	if err := store.NewRepos(w.cfg.DB).DeliveryOutbox.Enqueue(ctx, entry); err != nil {
		return "", err
	}
	w.wake()
	return key, nil
}

func idempotencyKey(ownerID, artifactID string, version int, target string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", ownerID, artifactID, version, target)))
	return hex.EncodeToString(sum[:])
}

// RunOnce 处理一轮待上传交付，返回成功上传数。
func (w *Worker) RunOnce(ctx context.Context) int {
	repos := store.NewRepos(w.cfg.DB)
	entries, err := repos.DeliveryOutbox.Due(ctx, w.cfg.Now(), 5)
	if err != nil {
		w.cfg.Logger("[delivery] 读取 Outbox 失败: %v", err)
		return 0
	}
	uploaded := 0
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		if err := repos.DeliveryOutbox.MarkUploading(ctx, entry.IdempotencyKey); err != nil {
			w.cfg.Logger("[delivery] 标记上传中失败: %v", err)
			continue
		}
		reader, version, err := w.cfg.Artifacts.OpenVersion(ctx, entry.OwnerUserID,
			entry.ArtifactID, entry.ArtifactVersion)
		if err != nil {
			_ = repos.DeliveryOutbox.MarkFailed(ctx, entry.IdempotencyKey, "读取 Artifact 版本失败："+err.Error())
			continue
		}
		delivery, uploadErr := w.cfg.Client.Upload(ctx, UploadRequest{
			ArtifactID: entry.ArtifactID, Version: entry.ArtifactVersion,
			TargetDeviceID: entry.TargetDeviceID, Name: version.OriginalName,
			MimeType: version.MimeType, SizeBytes: version.SizeBytes,
			SHA256: version.ContentHash, IdempotencyKey: entry.IdempotencyKey,
			AllowExecutable: entry.AllowExecutable, Reader: reader,
		})
		reader.Close()
		if uploadErr != nil {
			w.handleError(ctx, repos, entry, uploadErr)
			continue
		}
		if err := repos.DeliveryOutbox.MarkDone(ctx, entry.IdempotencyKey, delivery.DeliveryID, w.cfg.Now()); err != nil {
			w.cfg.Logger("[delivery] 标记完成失败: %v", err)
			continue
		}
		uploaded++
		if w.cfg.OnDelivered != nil {
			if err := w.cfg.OnDelivered(ctx, &entry, delivery); err != nil {
				w.cfg.Logger("[delivery] 交付摘要写入失败: %v", err)
			}
		}
		w.cfg.Logger("[delivery] uploaded artifact=%s v%d -> %s delivery=%s size=%d",
			entry.ArtifactID, entry.ArtifactVersion, entry.TargetDeviceID,
			delivery.DeliveryID, delivery.SizeBytes)
	}
	return uploaded
}

func (w *Worker) handleError(ctx context.Context, repos *store.Repos, entry store.DeliveryOutboxEntry, err error) {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case 400, 403, 404, 409, 413, 415, 422:
			_ = repos.DeliveryOutbox.MarkFailed(ctx, entry.IdempotencyKey, err.Error())
			w.cfg.Logger("[delivery] 交付失败（不可重试）: %v", err)
			return
		}
	}
	delay := 10 * time.Second
	for i := 0; i < entry.Attempts && i < 6; i++ {
		delay *= 2
	}
	if delay > 10*time.Minute {
		delay = 10 * time.Minute
	}
	_ = repos.DeliveryOutbox.MarkRetry(ctx, entry.IdempotencyKey, err.Error(), w.cfg.Now().Add(delay))
	w.cfg.Logger("[delivery] 上传失败，%s 后重试: %v", delay, err)
}
