package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func systemToolTC(workspace string) *ToolContext {
	return &ToolContext{Runtime: &Runtime{Tools: &ToolServices{Workspace: workspace}}}
}

func TestWriteThenReadFile(t *testing.T) {
	workspace := t.TempDir()
	tc := systemToolTC(workspace)
	write, err := (writeFileTool{}).Execute(context.Background(), json.RawMessage(`{"path":"nested/note.txt","content":"第一行\n第二行\n第三行"}`), tc)
	if err != nil || write.Status != StatusOK {
		t.Fatalf("write = %+v, err=%v", write, err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "nested", "note.txt"))
	if err != nil || string(raw) != "第一行\n第二行\n第三行" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	read, err := (readFileTool{}).Execute(context.Background(), json.RawMessage(`{"path":"nested/note.txt","offset":2,"limit":1}`), tc)
	if err != nil || read.Status != StatusOK {
		t.Fatalf("read = %+v, err=%v", read, err)
	}
	data := read.Data.(map[string]any)
	if data["content"] != "第二行" || data["returned_lines"] != 1 || data["total_lines"] != 3 || data["truncated"] != true {
		t.Fatalf("read data = %+v", data)
	}
}

func TestReadRejectsDirectory(t *testing.T) {
	res, err := (readFileTool{}).Execute(context.Background(), json.RawMessage(`{"path":"."}`), systemToolTC(t.TempDir()))
	if err != nil || res.Status != StatusError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestBashReturnsOutputAndExitCode(t *testing.T) {
	tc := systemToolTC(t.TempDir())
	res, err := (bashTool{}).Execute(context.Background(), json.RawMessage(`{"command":"echo goldenfinger","timeout_seconds":10}`), tc)
	if err != nil || res.Status != StatusOK {
		t.Fatalf("bash = %+v, err=%v", res, err)
	}
	data := res.Data.(map[string]any)
	if data["exit_code"] != 0 || !strings.Contains(data["output"].(string), "goldenfinger") {
		t.Fatalf("bash data = %+v", data)
	}
	command := "exit 7"
	if runtime.GOOS == "windows" {
		command = "exit /B 7"
	}
	args, _ := json.Marshal(map[string]any{"command": command, "timeout_seconds": 10})
	res, err = (bashTool{}).Execute(context.Background(), args, tc)
	if err != nil || res.Status != StatusOK || res.Data.(map[string]any)["exit_code"] != 7 {
		t.Fatalf("nonzero bash = %+v, err=%v", res, err)
	}
}

func TestBashRejectsInvalidWorkDir(t *testing.T) {
	workspace := t.TempDir()
	args, _ := json.Marshal(map[string]string{"command": "echo no", "work_dir": filepath.Join(workspace, "missing")})
	res, err := (bashTool{}).Execute(context.Background(), args, systemToolTC(workspace))
	if err != nil || res.Status != StatusError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestSystemToolsExposePiStyleNames(t *testing.T) {
	registry := NewRegistry(SystemTools()...)
	specs := registry.Specs()
	if len(specs) != 3 || specs[0].Name != "read" || specs[1].Name != "bash" || specs[2].Name != "write" {
		t.Fatalf("specs = %+v", specs)
	}
}
