package agenthub

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeBMAAddr(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:10011": "http://127.0.0.1:10011",
		":10011":          "http://127.0.0.1:10011",
		"http://x:1/":     "http://x:1",
		"":                "",
	}
	for input, want := range cases {
		if got := normalizeBMAAddr(input); got != want {
			t.Errorf("normalizeBMAAddr(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDiscoverBMAInstallReadsDesktopEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "launch.ps1"), []byte("# launcher"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := "HTTP_ADDR=127.0.0.1:10123\nBMA_API_TOKEN=test-token\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GFA_BMA_HOME", root)

	base, launcher, token := discoverBMAInstall(t.TempDir())
	if base != "http://127.0.0.1:10123" {
		t.Errorf("base = %q", base)
	}
	if launcher != filepath.Join(root, "app", "launch.ps1") || token != "test-token" {
		t.Errorf("launcher=%q token=%q", launcher, token)
	}
}

func TestDiscoverBMAMarksInstalledWhenOffline(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "launch.ps1")
	if err := os.WriteFile(launcher, []byte("# launcher"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		BMAURL:  "http://127.0.0.1:1",
		BMAPath: launcher,
		HTTP:    &http.Client{Timeout: time.Second},
	}
	agent := manager.discoverBMA(context.Background(), Agent{ID: "bma", Name: "BMA", Kind: "bma"})
	if !agent.Installed || agent.Running {
		t.Errorf("offline installed BMA = %+v", agent)
	}
	if !containsString(agent.Capabilities, "delegate") {
		t.Errorf("capabilities = %v", agent.Capabilities)
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
