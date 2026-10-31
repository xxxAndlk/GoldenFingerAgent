package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"goldenfinger/agent/internal/extsvc"
)

type fakeFetch struct {
	url  string
	page *extsvc.WebPage
}

func (f *fakeFetch) Fetch(_ context.Context, url string) (*extsvc.WebPage, error) {
	f.url = url
	return f.page, nil
}

func TestWebFetchTool(t *testing.T) {
	fetch := &fakeFetch{page: &extsvc.WebPage{Title: "标题", Content: "# 正文"}}
	tc := &ToolContext{Runtime: &Runtime{Tools: &ToolServices{Fetch: fetch}}}
	res, err := (webFetchTool{}).Execute(context.Background(), json.RawMessage(`{"url":" https://example.com "}`), tc)
	if err != nil || res.Status != StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if fetch.url != "https://example.com" {
		t.Fatalf("url=%q", fetch.url)
	}
	data := res.Data.(map[string]any)
	if data["provider"] != "deepseek" || data["content"] != "# 正文" || data["truncated"] != false {
		t.Fatalf("data=%+v", data)
	}
}

func TestWebFetchToolTruncatesLargePage(t *testing.T) {
	fetch := &fakeFetch{page: &extsvc.WebPage{Content: strings.Repeat("a", 50001)}}
	tc := &ToolContext{Runtime: &Runtime{Tools: &ToolServices{Fetch: fetch}}}
	res, err := (webFetchTool{}).Execute(context.Background(), json.RawMessage(`{"url":"https://example.com"}`), tc)
	if err != nil || res.Status != StatusOK || res.Data.(map[string]any)["truncated"] != true {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
