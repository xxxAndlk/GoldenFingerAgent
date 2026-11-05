package agenthub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

const maxDownloadedArtifact = 100 << 20

// ErrUnsafeArtifactURL 表示外部文件 URI 指向不允许的地址（本机/私网/非 HTTPS）。
var ErrUnsafeArtifactURL = errors.New("agenthub: artifact URL is not allowed")

func (m *Manager) materializeArtifacts(items []rawArtifact) ([]AgentArtifact, error) {
	out := make([]AgentArtifact, 0, len(items))
	for _, item := range items {
		artifact := item.AgentArtifact
		if artifact.ID == "" {
			artifact.ID = uuid.NewString()
		}
		if len(item.Raw) > 0 {
			path, err := m.saveInlineArtifact(artifact.Name, item.Raw)
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(item.Raw)
			artifact.LocalPath = path
			artifact.SHA256 = hex.EncodeToString(sum[:])
			artifact.Size = int64(len(item.Raw))
		}
		out = append(out, artifact)
	}
	return out, nil
}

func (m *Manager) saveInlineArtifact(name string, data []byte) (string, error) {
	dir := filepath.Join(m.Workspace, ".agent-artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	final := filepath.Join(dir, uuid.NewString()[:8]+"-"+safeArtifactName(name))
	if err := os.Rename(tmpPath, final); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return final, nil
}

// DownloadArtifact 下载外部 Agent 提供的 HTTPS URI 到本地目录。
// 只允许公网 HTTPS，禁止本机/私网/重定向到非法地址；下载后重新计算 SHA-256。
func (m *Manager) DownloadArtifact(ctx context.Context, artifact AgentArtifact, destDir string) (AgentArtifact, error) {
	raw := strings.TrimSpace(artifact.ExternalURI)
	if raw == "" {
		return artifact, errors.New("agenthub: artifact has no external URI")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return artifact, ErrUnsafeArtifactURL
	}
	if destDir == "" {
		destDir = filepath.Join(m.Workspace, ".agent-artifacts")
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return artifact, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return artifact, err
	}
	resp, err := safeArtifactClient().Do(req)
	if err != nil {
		return artifact, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return artifact, fmt.Errorf("agenthub: artifact download HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(destDir, ".download-*")
	if err != nil {
		return artifact, err
	}
	tmpPath := tmp.Name()
	fail := func(err error) (AgentArtifact, error) {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return artifact, err
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, maxDownloadedArtifact+1))
	if err != nil {
		return fail(err)
	}
	if n > maxDownloadedArtifact {
		return fail(fmt.Errorf("agenthub: artifact exceeds %d MiB", maxDownloadedArtifact>>20))
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return artifact, err
	}
	final := filepath.Join(destDir, uuid.NewString()[:8]+"-"+safeArtifactName(artifact.Name))
	if err := os.Rename(tmpPath, final); err != nil {
		_ = os.Remove(tmpPath)
		return artifact, err
	}
	sum := hasher.Sum(nil)
	actual := hex.EncodeToString(sum)
	if artifact.SHA256 != "" && !strings.EqualFold(artifact.SHA256, actual) {
		_ = os.Remove(final)
		return artifact, fmt.Errorf("agenthub: artifact sha256 mismatch (want %s, got %s)", artifact.SHA256, actual)
	}
	artifact.LocalPath = final
	artifact.Size = n
	artifact.SHA256 = actual
	return artifact, nil
}

// ImportedArtifact 描述已转为 WorkTask 交付物的外部文件。
type ImportedArtifact struct {
	ArtifactID string `json:"artifact_id"`
	Version    int    `json:"version"`
	Name       string `json:"name"`
	Path       string `json:"path"`
}

// ImportArtifacts 把已经落到本地的外部 Agent 文件转为 WorkTask Artifact（不可变快照）。
// 外部 URI 不会在这里自动下载：download_agent_artifact 工具必须先取得独立 Grant，
// 再调用 DownloadArtifact。这样委派 Agent 与访问外部 URL 是两次不同的授权。
// 失败项会汇总返回，调用方必须如实展示，不能声称已入库。
func (m *Manager) ImportArtifacts(ctx context.Context, svc *worktask.ArtifactService,
	ownerID, taskID, jobID, agentID string, items []AgentArtifact) ([]ImportedArtifact, error) {
	if svc == nil || ownerID == "" || taskID == "" {
		return nil, errors.New("agenthub: 导入文件需要 Artifact 服务、owner 与 WorkTask")
	}
	var imported []ImportedArtifact
	var errs []error
	for _, item := range items {
		path := item.LocalPath
		if path == "" && item.ExternalURI != "" {
			errs = append(errs, fmt.Errorf("%s: 外部 URL 文件需要 download_agent_artifact 独立授权", item.Name))
			continue
		}
		if path == "" {
			errs = append(errs, fmt.Errorf("%s: 没有可导入的本地文件", item.Name))
			continue
		}
		ref := "a2a:" + agentID + ":" + firstNonEmpty(item.ID, item.Name)
		artifact, version, err := svc.CreateFileDraft(ctx, worktask.CreateFileArtifactInput{
			OwnerID: ownerID, TaskID: taskID, Kind: store.ArtifactKindFile,
			Title: item.Name, Path: path,
			ChangeSummary: "外部 Agent " + agentID + " 返回的文件产物",
			SourceRefs:    []string{ref}, JobID: jobID,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", item.Name, err))
			continue
		}
		imported = append(imported, ImportedArtifact{
			ArtifactID: artifact.ID, Version: version.Version, Name: item.Name, Path: path,
		})
	}
	return imported, errors.Join(errs...)
}

func safeArtifactClient() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, item := range ips {
				if !isPublicIP(item.IP) {
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
				if dialErr == nil {
					return conn, nil
				}
			}
			return nil, ErrUnsafeArtifactURL
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return errors.New("agenthub: too many redirects")
			}
			if req.URL.Scheme != "https" {
				return ErrUnsafeArtifactURL
			}
			return nil
		},
	}
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// 云元数据地址属于链路本地，已被 IsLinkLocalUnicast 覆盖；这里再显式拦截。
	if ip.String() == "169.254.169.254" {
		return false
	}
	return true
}

func safeArtifactName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == '/' || r == '\\' || r == ':' {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 120 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." {
		return "artifact"
	}
	return out
}
