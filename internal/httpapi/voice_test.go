package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/voice"
)

func newVoiceServer(t *testing.T) (http.Handler, *voice.Service) {
	t.Helper()
	srv, _ := newTestServer(t)
	svc := voice.NewService(voice.Config{}, func() time.Time { return fixedNow })
	srv.Voice = svc
	return srv.Handler(), svc
}

type voiceResp struct {
	Actions []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"actions"`
	NextFlushMs int64    `json:"next_flush_ms"`
	Captured    []string `json:"captured"`
}

func TestVoiceTextPersistsCapturedSegment(t *testing.T) {
	h, _ := newVoiceServer(t)

	// 只是一句随口说的话：暂存，不返回动作。
	rec := postJSON(t, h, "/api/voice/text", `{"text":"明天要去医院"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var first voiceResp
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Actions) != 0 {
		t.Fatalf("应暂存而不产生动作: %+v", first.Actions)
	}
	if first.NextFlushMs != 4000 {
		t.Fatalf("next_flush_ms=%d", first.NextFlushMs)
	}

	// 4s 无后文 → 手机发 flush，由 Go 落库并返回片段 id。
	rec = postJSON(t, h, "/api/voice/text", `{"flush":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("flush status=%d body=%s", rec.Code, rec.Body)
	}
	var flushed voiceResp
	if err := json.Unmarshal(rec.Body.Bytes(), &flushed); err != nil {
		t.Fatal(err)
	}
	if len(flushed.Actions) != 1 || flushed.Actions[0].Type != voice.ActionCapture {
		t.Fatalf("actions=%+v", flushed.Actions)
	}
	if len(flushed.Captured) != 1 || flushed.Captured[0] == "" {
		t.Fatalf("captured=%v（片段应真正写入情景记忆）", flushed.Captured)
	}

	// 再 flush 一次不应重复写库。
	rec = postJSON(t, h, "/api/voice/text", `{"flush":true}`)
	var again voiceResp
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if len(again.Captured) != 0 {
		t.Fatalf("重复 flush 不应再落库: %v", again.Captured)
	}
}

func TestVoiceTextWakeWordChats(t *testing.T) {
	h, _ := newVoiceServer(t)
	rec := postJSON(t, h, "/api/voice/text", `{"text":"小金小金，现在几点了"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var resp voiceResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Actions) != 1 || resp.Actions[0].Type != voice.ActionChat {
		t.Fatalf("actions=%+v", resp.Actions)
	}
	if resp.Actions[0].Text != "现在几点了" {
		t.Fatalf("chat text=%q", resp.Actions[0].Text)
	}
}

func TestVoiceTextRejectsEmptyAndUnconfigured(t *testing.T) {
	h, _ := newVoiceServer(t)
	if rec := postJSON(t, h, "/api/voice/text", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body status=%d", rec.Code)
	}

	srv, _ := newTestServer(t) // Voice 未装配
	rec := postJSON(t, srv.Handler(), "/api/voice/text", `{"text":"随便说说"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestVoiceTextRequiresMemoryService(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.Voice = voice.NewService(voice.Config{}, func() time.Time { return fixedNow })
	srv.Runtime.Tools.Memory = nil // 模拟记忆服务缺失
	h := srv.Handler()

	if rec := postJSON(t, h, "/api/voice/text", `{"text":"随口一句"}`); rec.Code != http.StatusOK {
		t.Fatalf("暂存阶段不需要记忆服务，status=%d", rec.Code)
	}
	rec := postJSON(t, h, "/api/voice/text", `{"flush":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("落库失败必须如实报错，status=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "记忆服务不可用") {
		t.Fatalf("body=%s", rec.Body)
	}
}
