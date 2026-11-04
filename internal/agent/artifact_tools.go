package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"goldenfinger/agent/internal/llm"
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

// ArtifactTools 返回 WorkTask 专用交付物工具；主聊天不会看到它。
func ArtifactTools() []Tool {
	return []Tool{createArtifactTool{}}
}
