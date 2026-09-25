package settings

import (
	"path/filepath"
	"testing"
)

func TestSaveAndLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("open missing: %v", err)
	}
	if got := s.LLM(); got.APIKey != "" || got.Model != "" {
		t.Fatalf("want zero value, got %+v", got)
	}
	want := LLM{BaseURL: "https://api.deepseek.com", APIKey: "sk-x", Model: "deepseek-flash"}
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
