package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

type createArtifactArgs struct {
	ArtifactID    string   `json:"artifact_id,omitempty"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title,omitempty"`
	Content       string   `json:"content"`
	ChangeSummary string   `json:"change_summary,omitempty"`
	SourceRefs    []string `json:"source_refs,omitempty"`
}

type createArtifactTool struct{}

func (createArtifactTool) Spec() llm.ToolSpec {
	return toolSpec("create_artifact",
		"创建或追加 WorkTask 交付物版本。新交付物省略 artifact_id；修改已有交付物时传 artifact_id 与完整内容。内容会进入待用户验收的版本链，文件路径由服务端生成。",
		map[string]any{
			"artifact_id":    map[string]any{"type": "string", "description": "可选：已有交付物 ID；追加新版本时填写"},
			"kind":           map[string]any{"type": "string", "enum": []string{"text", "document", "code", "analysis", "email_draft", "external_ref"}},
			"title":          map[string]any{"type": "string"},
			"content":        map[string]any{"type": "string", "description": "完整交付内容"},
			"change_summary": map[string]any{"type": "string"},
			"source_refs":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "kind", "content")
}

func (createArtifactTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a createArtifactArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	if tc == nil || tc.Runtime == nil || tc.Runtime.Tools == nil || tc.Runtime.Tools.Artifacts == nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "WorkTask 交付物服务未启用"}}, nil
	}
	if tc.Runtime.WorkTaskID == "" || tc.Session == nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "create_artifact 只能在 WorkTask 中使用"}}, nil
	}
	if a.ArtifactID == "" {
		artifact, version, err := tc.Runtime.Tools.Artifacts.CreateDraft(ctx, worktask.CreateArtifactInput{
			OwnerID: tc.Session.UserID, TaskID: tc.Runtime.WorkTaskID, Kind: a.Kind,
			Title: a.Title, Content: a.Content, SourceRefs: a.SourceRefs, JobID: tc.Runtime.JobID,
		})
		if err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		return ToolResult{Status: StatusOK, Data: map[string]any{
			"artifact_id": artifact.ID, "version": version.Version, "status": artifact.Status,
			"reply": fmt.Sprintf("已创建交付物草稿 v%d，将在本段结束时进入验收。", version.Version),
		}}, nil
	}
	artifact, version, err := tc.Runtime.Tools.Artifacts.AddVersion(ctx, tc.Session.UserID, a.ArtifactID,
		-1, a.Content, a.ChangeSummary, a.SourceRefs, tc.Runtime.JobID)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"artifact_id": artifact.ID, "version": version.Version, "status": artifact.Status,
		"reply": fmt.Sprintf("已追加交付物 v%d。", version.Version),
	}}, nil
}

type createFileArtifactArgs struct {
	ArtifactID      string   `json:"artifact_id,omitempty"`
	Kind            string   `json:"kind,omitempty"`
	Title           string   `json:"title,omitempty"`
	Path            string   `json:"path"`
	ChangeSummary   string   `json:"change_summary,omitempty"`
	SourceRefs      []string `json:"source_refs,omitempty"`
	AllowExecutable bool     `json:"allow_executable,omitempty"`
}

type createFileArtifactTool struct{}

func (createFileArtifactTool) Spec() llm.ToolSpec {
	return toolSpec("create_file_artifact",
		"把本地文件保存为不可变 WorkTask 交付物版本（支持文本与二进制）。文件会复制到 Artifact 根目录并记录 SHA-256；后续源文件变化不影响已保存版本。可执行文件与脚本默认拒绝。",
		map[string]any{
			"artifact_id":      map[string]any{"type": "string", "description": "可选：已有交付物 ID；追加新版本时填写"},
			"kind":             map[string]any{"type": "string", "enum": []string{"file", "text", "document", "code", "analysis", "email_draft", "external_ref"}},
			"title":            map[string]any{"type": "string"},
			"path":             map[string]any{"type": "string", "description": "已授权本地文件路径（绝对路径或相对工作目录）"},
			"change_summary":   map[string]any{"type": "string"},
			"source_refs":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"allow_executable": map[string]any{"type": "boolean", "description": "确实需要交付可执行文件/脚本时才设为 true"},
		}, "path")
}

func (createFileArtifactTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a createFileArtifactArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	if tc == nil || tc.Runtime == nil || tc.Runtime.Tools == nil || tc.Runtime.Tools.Artifacts == nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "WorkTask 交付物服务未启用"}}, nil
	}
	if tc.Runtime.WorkTaskID == "" || tc.Session == nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "create_file_artifact 只能在 WorkTask 中使用"}}, nil
	}
	if a.ArtifactID == "" {
		artifact, version, err := tc.Runtime.Tools.Artifacts.CreateFileDraft(ctx, worktask.CreateFileArtifactInput{
			OwnerID: tc.Session.UserID, TaskID: tc.Runtime.WorkTaskID, Kind: a.Kind,
			Title: a.Title, Path: a.Path, ChangeSummary: a.ChangeSummary,
			SourceRefs: a.SourceRefs, JobID: tc.Runtime.JobID, AllowExecutable: a.AllowExecutable,
		})
		if err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		return fileArtifactResult(artifact.ID, version, "已保存文件交付物 v1")
	}
	artifact, version, err := tc.Runtime.Tools.Artifacts.AddFileVersion(ctx, tc.Session.UserID,
		a.ArtifactID, -1, a.Path, a.ChangeSummary, a.SourceRefs, tc.Runtime.JobID, a.AllowExecutable)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return fileArtifactResult(artifact.ID, version, fmt.Sprintf("已追加文件交付物 v%d", version.Version))
}

func fileArtifactResult(artifactID string, version *store.ArtifactVersion, message string) (ToolResult, error) {
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"artifact_id": artifactID, "version": version.Version, "name": version.OriginalName,
		"size_bytes": version.SizeBytes, "sha256": version.ContentHash,
		"mime_type": version.MimeType,
		"reply": fmt.Sprintf("%s（%s，%d 字节，sha256:%s）。", message, version.OriginalName,
			version.SizeBytes, version.ContentHash),
	}}, nil
}

type deliverArtifactArgs struct {
	ArtifactID      string `json:"artifact_id"`
	Version         int    `json:"version,omitempty"`
	TargetDeviceID  string `json:"target_device_id"`
	AllowExecutable bool   `json:"allow_executable,omitempty"`
}

type deliverArtifactTool struct{}

func (deliverArtifactTool) Spec() llm.ToolSpec {
	return toolSpec("deliver_artifact",
		"把已保存的 WorkTask Artifact 版本交付到指定设备（通常是手机）。只上传文件副本到云端临时交付区；文件正文不会进入聊天记录或长期记忆。交付是异步的，成功后可在手机“交付物”列表下载。",
		map[string]any{
			"artifact_id":      map[string]any{"type": "string", "description": "要交付的 Artifact ID"},
			"version":          map[string]any{"type": "integer", "description": "版本号；留空或 0 表示当前版本"},
			"target_device_id": map[string]any{"type": "string", "description": "目标设备 ID（手机云端设备 ID）"},
			"allow_executable": map[string]any{"type": "boolean", "description": "确实需要交付可执行文件/脚本时才设为 true"},
		}, "artifact_id", "target_device_id")
}

func (deliverArtifactTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a deliverArtifactArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	if tc == nil || tc.Runtime == nil || tc.Runtime.Tools == nil || tc.Runtime.Tools.Artifacts == nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "WorkTask 交付物服务未启用"}}, nil
	}
	if tc.Runtime.Tools.Delivery == nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "云端文件交付未启用，请先在设置中开启"}}, nil
	}
	if tc.Runtime.WorkTaskID == "" || tc.Session == nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "deliver_artifact 只能在 WorkTask 中使用"}}, nil
	}
	artifact, _, err := tc.Runtime.Tools.Artifacts.Get(ctx, tc.Session.UserID, a.ArtifactID)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	if artifact.TaskID != tc.Runtime.WorkTaskID {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "Artifact 不属于当前任务"}}, nil
	}
	key, err := tc.Runtime.Tools.Delivery.Enqueue(ctx, tc.Session.UserID, a.ArtifactID,
		a.Version, a.TargetDeviceID, a.AllowExecutable)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"status": "queued", "idempotency_key": key,
		"reply": "已加入交付队列；上传完成后会出现在目标设备的“交付物”列表。",
	}}, nil
}

// ArtifactTools 返回 WorkTask 专用交付物工具；主聊天不会看到它。
func ArtifactTools() []Tool {
	return []Tool{createArtifactTool{}, createFileArtifactTool{}, deliverArtifactTool{}}
}
