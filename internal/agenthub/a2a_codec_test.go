package agenthub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodecV03RequestShape(t *testing.T) {
	codec := codecFor("0.3")
	if codec.Method() != "message/send" || codec.Version() != "0.3" {
		t.Fatalf("codec=%s/%s", codec.Method(), codec.Version())
	}
	msg := codec.Message("你好")
	if msg["role"] != "user" || msg["kind"] != "message" {
		t.Fatalf("message=%v", msg)
	}
	parts := msg["parts"].([]map[string]any)
	if parts[0]["kind"] != "text" || parts[0]["text"] != "你好" {
		t.Fatalf("parts=%v", parts)
	}
}

func TestCodecV1RequestShape(t *testing.T) {
	codec := codecFor("1.0")
	if codec.Method() != "SendMessage" || codec.Version() != "1.0" {
		t.Fatalf("codec=%s/%s", codec.Method(), codec.Version())
	}
	msg := codec.Message("你好")
	if msg["role"] != "ROLE_USER" {
		t.Fatalf("role=%v", msg["role"])
	}
	parts := msg["parts"].([]map[string]any)
	if parts[0]["text"] != "你好" {
		t.Fatalf("parts=%v", parts)
	}
	if _, ok := parts[0]["kind"]; ok {
		t.Fatalf("v1 part should not carry kind: %v", parts[0])
	}
}

func TestParseV03TextAndFileBytes(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1",
		"result": map[string]any{
			"kind": "task", "id": "task-1",
			"status": map[string]any{"state": "completed"},
			"parts":  []map[string]any{{"kind": "text", "text": "完成"}},
			"artifacts": []map[string]any{{
				"artifactId": "a-1", "name": "报告.txt",
				"parts": []map[string]any{{
					"kind": "file",
					"file": map[string]any{
						"name": "报告.txt", "mimeType": "text/plain",
						"bytes": base64.StdEncoding.EncodeToString([]byte("文件内容")),
					},
				}},
			}},
		},
	})
	parsed, err := codecFor("0.3").Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Text != "完成" || parsed.State != "completed" || parsed.TaskID != "task-1" {
		t.Fatalf("parsed=%+v", parsed)
	}
	if len(parsed.Artifacts) != 1 || string(parsed.Artifacts[0].Raw) != "文件内容" {
		t.Fatalf("artifacts=%+v", parsed.Artifacts)
	}
	if parsed.Artifacts[0].ID != "a-1" || parsed.Artifacts[0].Name != "报告.txt" {
		t.Fatalf("artifact meta=%+v", parsed.Artifacts[0].AgentArtifact)
	}
}

func TestParseV1URLAndRaw(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "2",
		"result": map[string]any{
			"id":     "task-2",
			"status": map[string]any{"state": "TASK_STATE_COMPLETED"},
			"parts":  []map[string]any{{"text": "ok"}},
			"artifacts": []map[string]any{{
				"artifactId": "a-2", "name": "文件",
				"parts": []map[string]any{
					{"url": "https://example.com/f.bin", "mediaType": "application/octet-stream", "filename": "f.bin"},
					{"raw": base64.StdEncoding.EncodeToString([]byte("raw-bytes")), "mediaType": "text/plain", "filename": "r.txt"},
				},
			}},
		},
	})
	parsed, err := codecFor("1.0").Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.State != "completed" || parsed.Text != "ok" {
		t.Fatalf("parsed=%+v", parsed)
	}
	if len(parsed.Artifacts) != 2 {
		t.Fatalf("artifacts=%+v", parsed.Artifacts)
	}
	var uriFound, rawFound bool
	for _, artifact := range parsed.Artifacts {
		if artifact.ExternalURI == "https://example.com/f.bin" {
			uriFound = true
		}
		if string(artifact.Raw) == "raw-bytes" {
			rawFound = true
		}
	}
	if !uriFound || !rawFound {
		t.Fatalf("artifacts=%+v", parsed.Artifacts)
	}
}

func TestParseFileOnlyIsNotNoResult(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "3",
		"result": map[string]any{
			"id": "task-3", "status": map[string]any{"state": "TASK_STATE_COMPLETED"},
			"artifacts": []map[string]any{{
				"parts": []map[string]any{{"raw": base64.StdEncoding.EncodeToString([]byte("x")), "filename": "x.bin"}},
			}},
		},
	})
	parsed, err := codecFor("1.0").Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Text != "" || len(parsed.Artifacts) != 1 {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseRejectsOversizedInlineArtifact(t *testing.T) {
	big := make([]byte, maxInlineArtifact+1)
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "4",
		"result": map[string]any{
			"id": "task-4", "status": map[string]any{"state": "completed"},
			"artifacts": []map[string]any{{
				"parts": []map[string]any{{
					"raw": base64.StdEncoding.EncodeToString(big), "filename": "big.bin",
				}},
			}},
		},
	})
	if _, err := codecFor("1.0").Parse(body); err == nil {
		t.Fatal("oversized inline artifact should fail")
	}
}

func TestParseRejectsInlineHashMismatch(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "5",
		"result": map[string]any{
			"id": "task-5", "status": map[string]any{"state": "completed"},
			"artifacts": []map[string]any{{
				"parts": []map[string]any{{
					"raw":      base64.StdEncoding.EncodeToString([]byte("x")),
					"filename": "x.bin", "sha256": "deadbeef",
				}},
			}},
		},
	})
	if _, err := codecFor("1.0").Parse(body); err == nil {
		t.Fatal("inline sha256 mismatch should fail")
	}
}

func TestNormalizeTaskState(t *testing.T) {
	cases := map[string]string{
		"TASK_STATE_COMPLETED": "completed",
		"submitted":            "submitted",
		"input-required":       "input-required",
		"cancelled":            "canceled",
	}
	for input, want := range cases {
		if got := normalizeTaskState(input); got != want {
			t.Fatalf("normalize(%q)=%q want %q", input, got, want)
		}
	}
}

func TestDiscoverUsesInterfaceProtocolVersion(t *testing.T) {
	card := map[string]any{
		"name": "Fake", "url": "",
		"supportedInterfaces": []map[string]any{{
			"url": "https://agent.example.com/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0",
		}},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/.well-known/agent-card.json") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(card)
	}))
	defer ts.Close()
	manager := New(t.TempDir(), filepath.Join(t.TempDir(), "agents.json"))
	agent := manager.discoverA2A(context.Background(), Endpoint{ID: "fake", Name: "Fake", URL: ts.URL})
	if agent.ProtocolVersion != "1.0" {
		t.Fatalf("protocol version=%q", agent.ProtocolVersion)
	}
	if agent.Endpoint != "https://agent.example.com/a2a" {
		t.Fatalf("endpoint=%q", agent.Endpoint)
	}
}

func TestDiscoverDefaultsToV03(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "Legacy"})
	}))
	defer ts.Close()
	manager := New(t.TempDir(), filepath.Join(t.TempDir(), "agents.json"))
	agent := manager.discoverA2A(context.Background(), Endpoint{ID: "legacy", Name: "Legacy", URL: ts.URL})
	if agent.ProtocolVersion != "0.3" {
		t.Fatalf("protocol version=%q, want 0.3", agent.ProtocolVersion)
	}
	manager2 := New(t.TempDir(), filepath.Join(t.TempDir(), "agents.json"))
	agent2 := manager2.discoverA2A(context.Background(), Endpoint{
		ID: "legacy", Name: "Legacy", URL: ts.URL, ProtocolVersion: "1.0",
	})
	if agent2.ProtocolVersion != "1.0" {
		t.Fatalf("explicit version=%q", agent2.ProtocolVersion)
	}
}

func TestDownloadRejectsUnsafeURLs(t *testing.T) {
	manager := New(t.TempDir(), filepath.Join(t.TempDir(), "agents.json"))
	for _, uri := range []string{"http://example.com/f", "https://127.0.0.1/f", "https://localhost/f"} {
		_, err := manager.DownloadArtifact(context.Background(), AgentArtifact{ExternalURI: uri}, t.TempDir())
		if err == nil {
			t.Fatalf("uri %s should be rejected", uri)
		}
	}
}

func TestMaterializeArtifactsSavesInline(t *testing.T) {
	workspace := t.TempDir()
	manager := New(workspace, filepath.Join(t.TempDir(), "agents.json"))
	items, err := manager.materializeArtifacts([]rawArtifact{{
		AgentArtifact: AgentArtifact{Name: "a.txt", MediaType: "text/plain"},
		Raw:           []byte("hello"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].LocalPath == "" || items[0].SHA256 == "" || items[0].Size != 5 {
		t.Fatalf("items=%+v", items)
	}
	raw, err := os.ReadFile(items[0].LocalPath)
	if err != nil || string(raw) != "hello" {
		t.Fatalf("file=%q err=%v", raw, err)
	}
	if !strings.HasPrefix(items[0].LocalPath, workspace) {
		t.Fatalf("path outside workspace: %s", items[0].LocalPath)
	}
}
