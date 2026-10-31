package agent

import (
	"context"
	"strings"
	"testing"

	"goldenfinger/agent/internal/llm/mock"
)

func TestMultiTurnRequestKeepsStableCachePrefix(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.TextResponse("第一轮回复"),
		mock.TextResponse("第二轮回复"),
	)
	if _, err := Run(context.Background(), sess, "第一句话", rt); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), sess, "第二句话", rt); err != nil {
		t.Fatal(err)
	}
	calls := rt.LLM.(*mock.Scripted).Calls
	if len(calls) != 2 {
		t.Fatalf("calls=%d", len(calls))
	}
	first := calls[0].Messages
	second := calls[1].Messages
	if first[0].Content != rt.Prompt.Stable() || second[0].Content != first[0].Content {
		t.Fatal("system prefix changed across turns")
	}
	if strings.Contains(second[0].Content, "当前时间") || strings.Contains(second[0].Content, "关于这位用户") {
		t.Fatal("dynamic time or memory leaked into stable system prefix")
	}
	// 第二轮必须原样保留第一轮的用户输入和模型输出，形成可命中的完整前缀。
	if len(second) != 4 || second[1].Content != "第一句话" || second[2].Content != "第一轮回复" {
		t.Fatalf("previous turn prefix was rewritten: %+v", second)
	}
	current := second[len(second)-1].Content
	if !strings.Contains(current, "当前时间") || !strings.Contains(current, "第二句话") {
		t.Fatalf("dynamic context is not at the current turn tail: %q", current)
	}
	if sess.Messages[0].Content != "第一句话" || sess.Messages[2].Content != "第二句话" {
		t.Fatalf("dynamic context must not be persisted: %+v", sess.Messages)
	}
}
