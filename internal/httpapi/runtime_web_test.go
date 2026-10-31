package httpapi

import (
	"testing"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/extsvc/deepseek"
	"goldenfinger/agent/internal/settings"
)

func TestEffectiveRuntimeHotReloadsDeepSeekWebTools(t *testing.T) {
	store, err := settings.Open(t.TempDir() + "/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLLM(settings.LLM{BaseURL: "https://api.deepseek.com", APIKey: "web-secret", Model: "deepseek-chat", Protocol: "responses"}); err != nil {
		t.Fatal(err)
	}
	server := &Server{Runtime: &agent.Runtime{Tools: &agent.ToolServices{Workspace: t.TempDir()}}, Settings: store}
	runtime := server.effectiveRuntime()
	search, ok := runtime.Tools.Search.(*deepseek.Client)
	if !ok || search.APIKey != "web-secret" || search.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("search = %+v", runtime.Tools.Search)
	}
	fetch, ok := runtime.Tools.Fetch.(*deepseek.Client)
	if !ok || fetch != search {
		t.Fatalf("fetch = %+v", runtime.Tools.Fetch)
	}
	if runtime.Tools.Workspace == "" {
		t.Fatal("workspace must survive runtime copy")
	}
}
