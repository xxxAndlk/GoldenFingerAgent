package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudTokenUsesEncryptedVault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCloudToken("device-token-secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "device-token-secret") {
		t.Fatal("token must not be written to settings.json in plaintext")
	}
	if got := st.CloudToken(); got != "device-token-secret" {
		t.Fatalf("CloudToken=%q", got)
	}
	if !st.HasCloudToken() {
		t.Fatal("HasCloudToken=false")
	}
	if err := st.SaveCloudMemory(CloudMemory{
		Enabled: true, BaseURL: "https://cloud.example.com", TokenRef: "cloud-token.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.CloudToken(); got != "device-token-secret" {
		t.Fatalf("token lost after SaveCloudMemory: %q", got)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("open missing: %v", err)
	}
	if got := s.LLM(); got.APIKey != "" || got.Model != "" {
		t.Fatalf("want zero value, got %+v", got)
	}
	want := LLM{BaseURL: "https://api.deepseek.com", APIKey: "sk-x", Model: "deepseek-chat", Protocol: "chat_completions"}
	if err := s.SaveLLM(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.LLM(); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestExternalJSONEditHotReloadsAndInvalidEditKeepsLastValid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLLM(LLM{BaseURL: "https://one.example", Model: "one"}); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"llm":{"base_url":"https://two.example","api_key":"key","model":"two","protocol":"chat_completions"}}`)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.LLM(); got.Model != "two" || got.Protocol != "chat_completions" {
		t.Fatalf("hot reload=%+v", got)
	}
	if err := os.WriteFile(p, []byte(`{invalid`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.LLM(); got.Model != "two" {
		t.Fatalf("invalid edit replaced config=%+v", got)
	}
}
