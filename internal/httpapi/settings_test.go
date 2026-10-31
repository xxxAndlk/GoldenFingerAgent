package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/settings"
)

func TestLLMSettingsPreservesFallbackKeyAndUsesJSON(t *testing.T) {
	store, err := settings.Open(t.TempDir() + "/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Settings: store, FallbackLLM: settings.LLM{BaseURL: "https://api.deepseek.com", APIKey: "real-secret", Model: "deepseek-chat"}}
	req := httptest.NewRequest(http.MethodPut, "/api/settings/llm", strings.NewReader(`{"base_url":"https://api.deepseek.com","model":"deepseek-chat","api_key":"****cret","protocol":"chat_completions"}`))
	res := httptest.NewRecorder()
	s.handlePutLLMSettings(res, req)
	if res.Code != 200 {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	got := store.LLM()
	if got.APIKey != "real-secret" {
		t.Fatalf("masked key was saved: %q", got.APIKey)
	}
	if got.Protocol != "chat_completions" {
		t.Fatalf("protocol=%q", got.Protocol)
	}
	if !strings.Contains(res.Body.String(), `"settings_path"`) {
		t.Fatalf("missing settings path: %s", res.Body.String())
	}
}

func TestLLMSettingsRoundTripDoesNotExposeKey(t *testing.T) {
	store, err := settings.Open(t.TempDir() + "/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLLM(settings.LLM{BaseURL: "https://example.com", APIKey: "secret-key", Model: "model", Protocol: "responses"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Settings: store}
	res := httptest.NewRecorder()
	s.handleGetLLMSettings(res, httptest.NewRequest(http.MethodGet, "/api/settings/llm", nil))
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["api_key"] == "secret-key" {
		t.Fatal("real key exposed")
	}
	if body["has_key"] != true || body["protocol"] != "responses" {
		t.Fatalf("body=%v", body)
	}
}
