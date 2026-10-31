package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPoolLoadsMetadataAndContent(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bma", "---\nname: bma\ndescription: BMA 开发与部署指南\n---\n\n# 正文\n使用 POST /api/sessions。\n")

	pool := NewPool()
	loaded, skipped := pool.LoadDir(root)
	if loaded != 1 || len(skipped) != 0 {
		t.Fatalf("loaded=%d skipped=%v", loaded, skipped)
	}
	item := pool.Find("bma")
	if item == nil || item.Description != "BMA 开发与部署指南" {
		t.Fatalf("skill not loaded: %+v", item)
	}
	if !strings.Contains(item.Content, "POST /api/sessions") {
		t.Errorf("content = %q", item.Content)
	}
	meta := pool.MetadataBlock()
	if !strings.Contains(meta, "bma") || !strings.Contains(meta, "load_skill") {
		t.Errorf("metadata block = %q", meta)
	}
}

func TestPoolSkipsSkillWithoutDescription(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "broken", "---\nname: broken\n---\n正文\n")
	pool := NewPool()
	loaded, skipped := pool.LoadDir(root)
	if loaded != 0 || len(skipped) != 1 {
		t.Fatalf("loaded=%d skipped=%v", loaded, skipped)
	}
	if pool.Find("broken") != nil {
		t.Error("skill without description must not be loaded")
	}
}
