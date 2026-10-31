package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDesktopPersistsCredentialsAndReusesThem(t *testing.T) {
	hub := NewHub(Config{})
	server := httptest.NewServer(NewServer(hub, Config{}).Handler())
	defer server.Close()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "relay.json")
	d := &Desktop{BaseURL: server.URL, Name: "工作机", StatePath: statePath, Logger: func(string, ...any) {}}

	id, err := d.EnsureCredentials(context.Background())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if id == "" {
		t.Fatal("empty desktop id")
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	// Windows 不保留 POSIX 权限位，凭据由用户目录 ACL 保护；Unix 上必须 0600。
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode=%v want 0600", info.Mode().Perm())
	}

	// 第二台“进程”读同一文件，不应再注册新电脑。
	d2 := &Desktop{BaseURL: server.URL, Name: "工作机", StatePath: statePath, Logger: func(string, ...any) {}}
	id2, err := d2.EnsureCredentials(context.Background())
	if err != nil {
		t.Fatalf("ensure2: %v", err)
	}
	if id2 != id {
		t.Fatalf("id changed: %s vs %s", id2, id)
	}
	if stats := hub.Stats(); stats.Desktops != 1 {
		t.Fatalf("expected 1 desktop registration, got %+v", stats)
	}

	status := d2.Status()
	if status["configured"] != true || status["connected"] != false {
		t.Fatalf("status=%+v", status)
	}
	if _, ok := status["token"]; ok {
		t.Fatal("status must not leak token")
	}
}

func TestDesktopRepairsCorruptedStateFile(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "relay.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Desktop{BaseURL: "http://127.0.0.1:1", StatePath: statePath}
	if err := d.Load(); err == nil {
		t.Fatal("expected error for corrupted credentials")
	}
}

func TestDesktopPairCodeRequiresRegistration(t *testing.T) {
	hub := NewHub(Config{})
	server := httptest.NewServer(NewServer(hub, Config{}).Handler())
	defer server.Close()

	d := &Desktop{BaseURL: server.URL, StatePath: filepath.Join(t.TempDir(), "relay.json"), Logger: func(string, ...any) {}}
	code, expires, err := d.PairCode(context.Background())
	if err != nil {
		t.Fatalf("pair code: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code=%q", code)
	}
	if time.Until(expires) <= 0 {
		t.Fatalf("expiry in past: %v", expires)
	}
	channels, err := d.Channels(context.Background())
	if err != nil || len(channels) != 0 {
		t.Fatalf("channels=%v err=%v", channels, err)
	}
}

func TestExecuteLocalChatAndDelegate(t *testing.T) {
	var gotPath, gotBody string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		gotBody = string(raw)
		if r.URL.Path == "/api/agents/delegate" {
			_ = json.NewEncoder(w).Encode(map[string]string{"output": "已分派"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"reply": "好的"})
	}))
	defer local.Close()

	handler := ExecuteLocal(local.URL, nil)
	res, err := handler(context.Background(), json.RawMessage(`{"text":"整理下载目录"}`))
	if err != nil || !res.OK || res.Reply != "好的" {
		t.Fatalf("chat res=%+v err=%v", res, err)
	}
	if gotPath != "/api/chat" {
		t.Fatalf("path=%s", gotPath)
	}

	res, err = handler(context.Background(), json.RawMessage(`{"text":"跑测试","agent_id":"claude","work_dir":"D:/proj"}`))
	if err != nil || !res.OK || res.Reply != "已分派" {
		t.Fatalf("delegate res=%+v err=%v", res, err)
	}
	if gotPath != "/api/agents/delegate" || gotBody == "" {
		t.Fatalf("path=%s body=%s", gotPath, gotBody)
	}
}

func TestExecuteLocalReportsUpstreamError(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "模型未配置"})
	}))
	defer local.Close()

	res, err := ExecuteLocal(local.URL, nil)(context.Background(), json.RawMessage(`{"text":"x"}`))
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.OK || res.Error != "模型未配置" {
		t.Fatalf("res=%+v", res)
	}
}
