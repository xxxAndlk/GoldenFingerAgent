package wechatdb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeFileDetectsSameSizeChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "message_0.db")
	page := make([]byte, 2*PageSize)
	for i := range page {
		page[i] = byte(i)
	}
	if err := os.WriteFile(path, page, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	first, err := probeFile(path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	// 文件大小不变但首页内容变了：checkpoint 后必须能发现。
	page[10] ^= 0xff
	if err := os.WriteFile(path, page, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	second, err := probeFile(path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if first.size != second.size {
		t.Fatalf("size changed: %d %d", first.size, second.size)
	}
	if !first.changed(second) {
		t.Fatal("same-size content change must be detected")
	}
	if first.changed(first) {
		t.Fatal("unchanged file must not be reported as changed")
	}
}

func TestRefreshWithoutDatabaseIsNoop(t *testing.T) {
	dir := t.TempDir()
	r := &Reader{
		AccountDir: filepath.Join(dir, "missing"),
		WorkDir:    filepath.Join(dir, "work"),
		Keys:       map[string]KeyBlob{},
		states:     map[string]fileState{},
	}
	if err := r.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}
