package agenthub

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func catalogForTest(t *testing.T, id string) catalogEntry {
	t.Helper()
	for _, entry := range catalog {
		if entry.id == id {
			return entry
		}
	}
	t.Fatalf("catalog entry %s not found", id)
	return catalogEntry{}
}

func TestDesktopShortcutDiscoveryEnablesOpenWithoutRunning(t *testing.T) {
	manager := testManager(t)
	manager.FindDesktop = func(_ context.Context, entry catalogEntry) string {
		if entry.id == "qoder" {
			return `C:\Apps\Qoder\Qoder Launcher.exe`
		}
		return ""
	}
	agents := manager.Scan(context.Background())
	var qoder Agent
	for _, agent := range agents {
		if agent.ID == "qoder" {
			qoder = agent
		}
	}
	if !qoder.Installed || qoder.Running || !canCall(qoder, "open") || canCall(qoder, "desktop_delegate") {
		t.Fatalf("Qoder = %+v", qoder)
	}
}

func TestOpenUsesDiscoveredDesktopExecutable(t *testing.T) {
	manager := testManager(t)
	manager.FindDesktop = func(_ context.Context, entry catalogEntry) string {
		if entry.id == "workbuddy" {
			return `D:\app\WorkBuddy\WorkBuddy.exe`
		}
		return ""
	}
	var opened Agent
	manager.OpenDesktop = func(_ context.Context, agent Agent) (Invocation, error) {
		opened = agent
		return Invocation{AgentID: agent.ID, Kind: agent.Kind, State: "opened", Output: "已打开 WorkBuddy"}, nil
	}
	result, err := manager.Open(context.Background(), "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Executable != `D:\app\WorkBuddy\WorkBuddy.exe` || result.State != "opened" {
		t.Fatalf("opened = %+v, result = %+v", opened, result)
	}
}

func TestOpenStartsCLIInWorkspace(t *testing.T) {
	manager := testManager(t)
	manager.Workspace = t.TempDir()
	manager.LookPath = func(command string) (string, error) {
		if command == "claude" {
			return `C:\Users\test\AppData\Roaming\npm\claude.cmd`, nil
		}
		return "", exec.ErrNotFound
	}
	var opened Agent
	var openedDir string
	manager.OpenCLI = func(_ context.Context, agent Agent, workDir string) (Invocation, error) {
		opened, openedDir = agent, workDir
		return Invocation{AgentID: agent.ID, Kind: agent.Kind, State: "opened"}, nil
	}
	result, err := manager.Open(context.Background(), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Executable == "" || openedDir != manager.Workspace || result.State != "opened" {
		t.Fatalf("opened = %+v, dir = %q, result = %+v", opened, openedDir, result)
	}
}

func TestClaudeAndPiUseDedicatedCLIArguments(t *testing.T) {
	claude := catalogForTest(t, "claude-code")
	if got, want := cliCommandArgs(claude, "修复测试"), []string{"--print", "--output-format", "json", "修复测试"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude args = %#v, want %#v", got, want)
	}
	pi := catalogForTest(t, "pi")
	if got, want := cliCommandArgs(pi, "检查项目"), []string{"--print", "检查项目"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pi args = %#v, want %#v", got, want)
	}
}

func TestClaudeJSONOutputReturnsResultAndSession(t *testing.T) {
	agent := Agent{ID: "claude-code", Kind: "cli"}
	result := parseCLIOutput(agent, `{"result":"修复完成","session_id":"session-1","is_error":false}`, "json")
	if result.Output != "修复完成" || result.TaskID != "session-1" || result.State != "completed" {
		t.Fatalf("result = %+v", result)
	}
	failed := parseCLIOutput(agent, `{"result":"权限不足","is_error":true}`, "json")
	if failed.State != "error" || failed.Output != "权限不足" {
		t.Fatalf("failed = %+v", failed)
	}
}
