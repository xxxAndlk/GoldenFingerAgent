package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goldenfinger/agent/internal/skill"
)

func TestSkillToolsListAndLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "bma")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: bma\ndescription: BMA 指南\n---\n正文：POST /api/sessions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := skill.NewPool()
	if _, skipped := pool.LoadDir(root); len(skipped) != 0 {
		t.Fatal(skipped)
	}
	tools := SkillTools(pool)
	list := tools[0]
	result, err := list.Execute(context.Background(), json.RawMessage(`{}`), &ToolContext{})
	if err != nil || result.Status != StatusOK {
		t.Fatalf("list skills: %+v %v", result, err)
	}
	payload, _ := json.Marshal(result.Data)
	if !strings.Contains(string(payload), "BMA 指南") {
		t.Errorf("list payload = %s", payload)
	}

	load := tools[1]
	result, err = load.Execute(context.Background(), json.RawMessage(`{"name":"bma"}`), &ToolContext{})
	if err != nil || result.Status != StatusOK {
		t.Fatalf("load skill: %+v %v", result, err)
	}
	payload, _ = json.Marshal(result.Data)
	if !strings.Contains(string(payload), "POST /api/sessions") {
		t.Errorf("load payload = %s", payload)
	}
}
