package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
)

func newCloudMemoryTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	ctx := t.Context()
	db, err := store.Connect(ctx, filepath.Join(t.TempDir(), "gfa.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	st, err := settings.Open(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Repos: store.NewRepos(db), Settings: st}, settingsPath
}

func TestCloudMemorySettingsStoresTokenInVault(t *testing.T) {
	server, settingsPath := newCloudMemoryTestServer(t)
	body, _ := json.Marshal(cloudMemoryRequest{
		Enabled: true, BaseURL: "https://cloud.example.com", DeviceID: "phone-1", Token: "device-token-123",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/settings/cloud-memory", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handlePutCloudMemory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT code=%d body=%s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "device-token-123") {
		t.Fatal("device token written to settings.json in plaintext")
	}
	if server.Settings.CloudToken() != "device-token-123" {
		t.Fatalf("vault token=%q", server.Settings.CloudToken())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings/cloud-memory", nil)
	rec = httptest.NewRecorder()
	server.handleGetCloudMemory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code=%d", rec.Code)
	}
	var dto map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto["has_token"] != true || dto["token"] != "" {
		t.Fatalf("dto=%v", dto)
	}
}

func TestCloudMemoryEnableRequiresToken(t *testing.T) {
	server, _ := newCloudMemoryTestServer(t)
	body, _ := json.Marshal(cloudMemoryRequest{Enabled: true, BaseURL: "https://cloud.example.com"})
	req := httptest.NewRequest(http.MethodPut, "/api/settings/cloud-memory", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handlePutCloudMemory(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, want 400", rec.Code)
	}
}
