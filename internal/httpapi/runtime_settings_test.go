package httpapi

import (
	"os"
	"testing"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm/openai"
	"goldenfinger/agent/internal/settings"
)

func TestEffectiveRuntimeHotReloadsJSONProtocolAndModel(t *testing.T) {
	path := t.TempDir() + "/settings.json"
	store, err := settings.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLLM(settings.LLM{BaseURL: "https://one.example", Model: "one", Protocol: "responses"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Runtime: &agent.Runtime{}, Settings: store}
	first := s.effectiveRuntime().LLM.(*openai.Client)
	if first.Model != "one" || first.Protocol != "responses" {
		t.Fatalf("first=%+v", first)
	}
	raw := []byte(`{"llm":{"base_url":"https://api.deepseek.com","api_key":"","model":"deepseek-chat","protocol":"responses"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	second := s.effectiveRuntime().LLM.(*openai.Client)
	if second.Model != "deepseek-chat" || second.Protocol != "responses" {
		t.Fatalf("second=%+v", second)
	}
}
