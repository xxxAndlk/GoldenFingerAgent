package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/settings"
)

func TestCollectionToolsRegistrationRiskAndWebConfiguration(t *testing.T) {
	sess, rt, user := testHarness(t)
	settingsStore, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager := collection.New(rt.Tools.Repos, settingsStore)
	registry := NewRegistry(CollectionTools(manager)...)
	if risk, grant := classifyToolName("list_collection_channels"); risk != RiskRead || grant {
		t.Fatalf("list risk=%s grant=%v", risk, grant)
	}
	if risk, grant := classifyToolName("configure_collection_channel"); risk != RiskSensitive || !grant {
		t.Fatalf("configure risk=%s grant=%v", risk, grant)
	}
	for _, tool := range DefaultTools() {
		name := tool.Spec().Name
		if name == "list_collection_channels" || name == "configure_collection_channel" {
			t.Fatalf("Android 默认工具不应包含 %s", name)
		}
	}
	tc := &ToolContext{Session: sess, Runtime: rt}
	result, err := registry.Execute(context.Background(), llmCall("configure_collection_channel", `{"channel":"web","action":"add","url":"https://example.com/current"}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("configure=%v err=%v", result, err)
	}
	if len(settingsStore.Web().Sites) != 1 {
		t.Fatal("网页配置没有持久化")
	}
	result, err = registry.Execute(context.Background(), llmCall("list_collection_channels", `{}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("list=%v err=%v", result, err)
	}
	raw, _ := json.Marshal(result.Data)
	if !json.Valid(raw) || user.ID == "" {
		t.Fatalf("data=%s", raw)
	}
}

func llmCall(name, arguments string) llm.ToolCall {
	return llm.ToolCall{Name: name, Arguments: json.RawMessage(arguments)}
}
