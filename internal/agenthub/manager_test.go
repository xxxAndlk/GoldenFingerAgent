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

func TestDelegateBMADoesNotRescanAgents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/sessions":
			_, _ = w.Write([]byte(`{"id":"bma-1","status":"completed","result":"done"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := testManager(t)
	manager.BMAURL = server.URL
	scans := 0
	manager.Processes = func(context.Context) ([]Process, error) { scans++; return nil, nil }
	inv, err := manager.DelegateAt(context.Background(), "bma", "测试任务", "")
	if err != nil {
		t.Fatalf("DelegateAt: %v", err)
	}
	if inv.Output != "done" || scans != 0 {
		t.Fatalf("BMA delegate = %+v scans=%d", inv, scans)
	}
}

func TestDelegateBMAAutoApprovesPlan(t *testing.T) {
	clarified := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/sessions" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"s1","status":"running"}`))
		case r.URL.Path == "/api/sessions/s1/clarify":
			clarified = true
			_, _ = w.Write([]byte(`{"session_id":"s1","status":"running"}`))
		case r.URL.Path == "/api/sessions/s1" && clarified:
			_, _ = w.Write([]byte(`{"id":"s1","status":"completed","result":"计划已执行"}`))
		case r.URL.Path == "/api/sessions/s1":
			_, _ = w.Write([]byte(`{"id":"s1","status":"awaiting_clarify","state":{"pending_clarify":{"id":"ask-1","question":"【计划确认】执行前请确认","options":[{"id":"approve","label":"批准开工"},{"id":"revise","label":"需要修改"}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := testManager(t)
	manager.BMAURL = server.URL
	scans := 0
	manager.Processes = func(context.Context) ([]Process, error) { scans++; return nil, nil }
	inv, err := manager.DelegateAt(context.Background(), "bma", "测试任务", "")
	if err != nil {
		t.Fatalf("DelegateAt: %v", err)
	}
	if !clarified || inv.Output != "计划已执行" || scans != 0 {
		t.Fatalf("auto approve = %+v clarified=%v scans=%d", inv, clarified, scans)
	}
}

func TestScanUsesCacheUntilRefresh(t *testing.T) {
	manager := testManager(t)
	scans := 0
	manager.Processes = func(context.Context) ([]Process, error) {
		scans++
		return []Process{{Name: "Kimi", Path: `D:\app\KimiWork\Kimi.exe`}}, nil
	}
	first := manager.Scan(context.Background())
	second := manager.Scan(context.Background())
	if scans != 1 {
		t.Fatalf("cached Scan should scan once, got %d", scans)
	}
	if len(first) != len(second) {
		t.Fatalf("cached length mismatch: %d vs %d", len(first), len(second))
	}
	manager.Refresh(context.Background())
	if scans != 2 {
		t.Fatalf("Refresh should scan again, got %d", scans)
	}
	manager.InvalidateCache()
	manager.Scan(context.Background())
	if scans != 3 {
		t.Fatalf("invalidated cache should scan again, got %d", scans)
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
			// 卡片未声明版本 → 按 v0.3 报文发送，不能把旧式报文标成 1.0。
			authorized = r.Header.Get("Authorization") == "Bearer secret" && r.Header.Get("A2A-Version") == "0.3"
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

func TestA2AV1Negotiation(t *testing.T) {
	var sawVersion, sawMethod, sawRole string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "V1 Agent",
				"supportedInterfaces": []map[string]string{{
					"url": server.URL + "/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0",
				}},
			})
		case "/a2a":
			sawVersion = r.Header.Get("A2A-Version")
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			sawMethod, _ = request["method"].(string)
			if params, ok := request["params"].(map[string]any); ok {
				if message, ok := params["message"].(map[string]any); ok {
					sawRole, _ = message["role"].(string)
				}
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"id":"t1","status":{"state":"TASK_STATE_COMPLETED"},"parts":[{"text":"v1 ok"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := testManager(t)
	agent, err := manager.RegisterA2A(Endpoint{ID: "v1", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if agent.ProtocolVersion != "1.0" {
		t.Fatalf("protocol version=%q", agent.ProtocolVersion)
	}
	result, err := manager.Delegate(context.Background(), "v1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "v1 ok" || sawVersion != "1.0" || sawMethod != "SendMessage" || sawRole != "ROLE_USER" {
		t.Fatalf("result=%+v version=%q method=%q role=%q", result, sawVersion, sawMethod, sawRole)
	}
}

func TestToolSchemasAreValidJSON(t *testing.T) {
	for _, tool := range Tools(testManager(t)) {
		if !json.Valid(tool.Spec().Parameters) {
			t.Errorf("%s schema invalid: %s", tool.Spec().Name, tool.Spec().Parameters)
		}
	}
}
