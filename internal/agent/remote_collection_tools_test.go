package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/llm"
)

// fakeRemoteCollection 记录手机端远程采集工具发出的命令。
type fakeRemoteCollection struct {
	listData map[string]any
	question string
	err      error
	applied  []collection.ConfigureInput
}

func (f *fakeRemoteCollection) ListCollectionChannels(_ context.Context, _ string) (map[string]any, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.listData, nil
}

func (f *fakeRemoteCollection) ConfigureCollectionChannel(_ context.Context, _ string, input collection.ConfigureInput) (map[string]any, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	f.applied = append(f.applied, input)
	return map[string]any{"channel": input.Channel, "action": input.Action}, f.question, nil
}

func remoteCollectionCall(name, args string) llm.ToolCall {
	return llm.ToolCall{Name: name, Arguments: json.RawMessage(args)}
}

func TestRemoteCollectionToolsCallTheDesktop(t *testing.T) {
	client := &fakeRemoteCollection{listData: map[string]any{"wechat_desktop": map[string]any{"enabled": true}}}
	registry := NewRegistry(RemoteCollectionTools(client)...)
	ctx := &ToolContext{Session: &Session{UserID: "u-1"}}

	result, err := registry.Execute(context.Background(), remoteCollectionCall("list_collection_channels", `{}`), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusOK {
		t.Fatalf("status=%v data=%v", result.Status, result.Data)
	}
	if result.Data == nil {
		t.Fatal("没有返回渠道数据")
	}

	result, err = registry.Execute(context.Background(),
		remoteCollectionCall("configure_collection_channel", `{"channel":"web","action":"add","url":"https://example.com/docs"}`), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusOK {
		t.Fatalf("status=%v data=%v", result.Status, result.Data)
	}
	if len(client.applied) != 1 {
		t.Fatalf("applied=%v", client.applied)
	}
	applied := client.applied[0]
	if applied.Channel != "web" || applied.Action != "add" || applied.URL != "https://example.com/docs" {
		t.Fatalf("参数没有原样传到电脑端：%+v", applied)
	}
}

func TestRemoteCollectionToolAsksForClarification(t *testing.T) {
	client := &fakeRemoteCollection{question: "有两个同名候选，请选择"}
	registry := NewRegistry(RemoteCollectionTools(client)...)
	ctx := &ToolContext{Session: &Session{UserID: "u-1"}}

	result, err := registry.Execute(context.Background(),
		remoteCollectionCall("configure_collection_channel", `{"channel":"wechat_desktop","action":"add","query":"老王"}`), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusClarify {
		t.Fatalf("status=%v", result.Status)
	}
	if result.Question == "" {
		t.Fatal("没有把澄清问题交给机主")
	}
}

func TestRemoteCollectionToolReportsDesktopFailure(t *testing.T) {
	client := &fakeRemoteCollection{err: errors.New("电脑当前离线")}
	registry := NewRegistry(RemoteCollectionTools(client)...)
	ctx := &ToolContext{Session: &Session{UserID: "u-1"}}

	result, err := registry.Execute(context.Background(),
		remoteCollectionCall("configure_collection_channel", `{"channel":"web","action":"enable"}`), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusError {
		t.Fatalf("status=%v", result.Status)
	}
}

// 远程配置采集渠道属于敏感动作：必须沿用电脑端工具的授权约束。
func TestRemoteCollectionToolKeepsSensitiveRisk(t *testing.T) {
	risk, grant := classifyToolName("configure_collection_channel")
	if risk != RiskSensitive || !grant {
		t.Fatalf("risk=%v grant=%v", risk, grant)
	}
	registry := NewRegistry(RemoteCollectionTools(&fakeRemoteCollection{})...)
	desc, ok := registry.Descriptor("configure_collection_channel")
	if !ok {
		t.Fatal("远程采集工具没有注册")
	}
	if desc.Risk != RiskSensitive || !desc.RequiresGrant {
		t.Fatalf("desc=%+v", desc)
	}
}
