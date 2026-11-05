package agenthub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
)

func TestDownloadAgentArtifactRequiresIndependentGrant(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	runtime := &agent.Runtime{
		WorkTaskID: f.task.ID, ToolProfile: store.WorkTaskProfileExternal,
		Tools: &agent.ToolServices{Artifacts: f.artifacts, Repos: f.repos},
	}
	tc := &agent.ToolContext{Session: &agent.Session{UserID: f.user.ID}, Runtime: runtime}
	registry := agent.NewRegistry(Tools(f.manager)...)
	args := json.RawMessage(`{"agent_id":"agent-x","name":"report.txt","external_uri":"http://127.0.0.1/report.txt"}`)

	result, err := registry.Execute(ctx, llm.ToolCall{ID: "download-denied", Name: "download_agent_artifact", Arguments: args}, tc)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusError || !strings.Contains(fmt.Sprint(result.Data), "有效授权") {
		t.Fatalf("download without grant must be denied before URL validation: %+v", result)
	}

	grant := &store.ActionGrant{
		OwnerUserID: f.user.ID, TaskID: f.task.ID, ActionType: "download_agent_artifact",
		ResourceScope: json.RawMessage(`{"external_uri":"http://127.0.0.1/report.txt"}`),
		RiskLevel:     store.GrantSensitive, RemainingUses: 1,
	}
	if err := f.repos.Grants.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	result, err = registry.Execute(ctx, llm.ToolCall{ID: "download-authorized", Name: "download_agent_artifact", Arguments: args}, tc)
	if err != nil {
		t.Fatal(err)
	}
	// 到达 URL 安全校验才说明独立 Grant 已通过；HTTP 仍必须被拦截。
	if result.Status != agent.StatusError || !strings.Contains(fmt.Sprint(result.Data), ErrUnsafeArtifactURL.Error()) {
		t.Fatalf("authorized download should reach URL safety check: %+v", result)
	}
	if list, _ := f.repos.Artifacts.ListByTask(ctx, f.user.ID, f.task.ID); len(list) != 0 {
		t.Fatalf("rejected URL must not create artifacts: %+v", list)
	}
}

func TestDelegateGrantDoesNotCoverExternalArtifactDownload(t *testing.T) {
	f := newImportFixture(t)
	inv := &Invocation{
		AgentID: "agent-x", Output: "done",
		Artifacts: []AgentArtifact{{Name: "remote.txt", ExternalURI: "https://example.com/remote.txt"}},
	}
	// 没有 WorkTask 时只能保留 URL 元数据，不能因委派已成功就自动下载。
	f.manager.attachArtifacts(context.Background(), nil, inv)
	if !strings.Contains(inv.Output, "download_agent_artifact 独立授权") {
		t.Fatalf("output=%q", inv.Output)
	}
}
