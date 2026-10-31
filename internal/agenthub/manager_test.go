package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	return &Manager{
		Workspace: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "agents.json"), HTTP: http.DefaultClient,
		LookPath:  func(string) (string, error) { return "", errors.New("not found") },
		ProbeCLI:  func(context.Context, string, string) (string, error) { return "test-version", nil },
		Processes: func(context.Context) ([]Process, error) { return nil, nil },
	}
}

func TestScanFindsDesktopCLIAndBMA(t *testing.T) {
	manager := testManager(t)
	if err := os.MkdirAll(filepath.Join(manager.Workspace, ".bma"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.Workspace, ".bma", "PROJECT.md"), []byte("BMA"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return `C:\tools\claude.exe`, nil
		}
		return "", errors.New("not found")
	}
	manager.Processes = func(context.Context) ([]Process, error) {
		return []Process{{Name: "Kimi", Path: `D:\app\KimiWork\Kimi.exe`}, {Name: "WorkBuddy"}, {Name: "bma-server"}}, nil
	}
	agents := manager.Scan(context.Background())
	byID := map[string]Agent{}
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	if !byID["kimiwork"].Running || !byID["workbuddy"].Running {
		t.Fatalf("desktop agents = %+v", byID)
	}
	if !byID["claude-code"].Installed {
		t.Fatalf("Claude = %+v", byID["claude-code"])
	}
	if !byID["bma"].Installed || !byID["bma"].Running {
		t.Fatalf("BMA = %+v", byID["bma"])
	}
	if !canCall(byID["kimiwork"], "desktop_delegate") || !canCall(byID["claude-code"], "delegate") {
		t.Fatalf("capabilities missing: %+v", byID)
	}
}

func canCall(agent Agent, capability string) bool {
	for _, item := range agent.Capabilities {
		if item == capability {
			return true
		}
	}
	return false
}

func TestBMAA2ADiscoveryAndDelegate(t *testing.T) {
	var authorized bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "BMA runtime", "description": "项目执行 Agent",
				"supportedInterfaces": []map[string]string{{"url": server.URL + "/a2a", "protocolBinding": "JSONRPC"}},
				"skills":              []map[string]string{{"id": "build", "name": "项目构建"}},
			})
		case "/a2a":
			authorized = r.Header.Get("Authorization") == "Bearer secret" && r.Header.Get("A2A-Version") == "1.0"
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request["method"] != "message/send" {
				t.Errorf("method = %v", request["method"])
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"kind":"task","id":"task-1","status":{"state":"completed"},"artifacts":[{"parts":[{"kind":"text","text":"BMA 已完成构建"}]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := testManager(t)
	agent, err := manager.RegisterA2A(Endpoint{ID: "bma", URL: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !agent.Running || agent.Kind != "bma" || !canCall(agent, "项目构建") {
		t.Fatalf("agent = %+v", agent)
	}
	result, err := manager.Delegate(context.Background(), "bma", "运行项目测试")
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "BMA 已完成构建" || result.TaskID != "task-1" || !authorized {
		t.Fatalf("result = %+v, authorized = %v", result, authorized)
	}
}

func TestToolSchemasAreValidJSON(t *testing.T) {
	for _, tool := range Tools(testManager(t)) {
		if !json.Valid(tool.Spec().Parameters) {
			t.Errorf("%s schema invalid: %s", tool.Spec().Name, tool.Spec().Parameters)
		}
	}
}
