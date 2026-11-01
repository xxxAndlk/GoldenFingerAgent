package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/settings"
)

func TestEmailSettingsMaskAndPersist(t *testing.T) {
	srv, _ := newTestServer(t)
	store, err := settings.Open(t.TempDir() + "/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	srv.Settings = store
	h := srv.Handler()

	rec := getJSON(t, h, "/api/settings/email")
	if rec.Code != http.StatusOK {
		t.Fatalf("get=%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password\":\"secret") {
		t.Fatalf("password leaked: %s", rec.Body)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings/email", strings.NewReader(`{
		"enabled":true,"host":"imap.example.com","port":993,"username":"me@example.com",
		"password":"secret-app-password","mailbox":"INBOX","tls":true,"initial_fetch":10,"poll_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	saved := httptest.NewRecorder()
	h.ServeHTTP(saved, req)
	if saved.Code != http.StatusOK {
		t.Fatalf("put=%d %s", saved.Code, saved.Body)
	}
	if strings.Contains(saved.Body.String(), "secret-app-password") {
		t.Fatalf("password echoed: %s", saved.Body)
	}
	cfg := store.Email()
	if !cfg.Enabled || cfg.Host != "imap.example.com" || cfg.Password != "secret-app-password" {
		t.Fatalf("stored=%+v", cfg)
	}

	// 掩码或空密码表示保留原密码。
	req = httptest.NewRequest(http.MethodPut, "/api/settings/email", strings.NewReader(`{
		"enabled":true,"host":"imap.example.com","port":993,"username":"me@example.com",
		"password":"","mailbox":"INBOX","tls":true}`))
	req.Header.Set("Content-Type", "application/json")
	saved = httptest.NewRecorder()
	h.ServeHTTP(saved, req)
	if saved.Code != http.StatusOK {
		t.Fatalf("second put=%d %s", saved.Code, saved.Body)
	}
	if store.Email().Password != "secret-app-password" {
		t.Fatal("blank password must preserve the existing secret")
	}
}
