package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"goldenfinger/agent/internal/extsvc"
)

type fakeSearch struct {
	query string
	count int
	res   []extsvc.SearchResult
	err   error
}

func (f *fakeSearch) Search(ctx context.Context, query string, count int) ([]extsvc.SearchResult, error) {
	f.query = query
	f.count = count
	return f.res, f.err
}

func webSearchTC(svc extsvc.SearchService) *ToolContext {
	return &ToolContext{Runtime: &Runtime{Tools: &ToolServices{Search: svc}}}
}

func TestWebSearchToolOK(t *testing.T) {
	fs := &fakeSearch{res: []extsvc.SearchResult{{Name: "标题", URL: "https://a.example", Snippet: "摘要"}}}
	res, err := (webSearchTool{}).Execute(context.Background(), json.RawMessage(`{"query":"  今天天气  ","count":3}`), webSearchTC(fs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %s", res.Status)
	}
	if fs.query != "今天天气" || fs.count != 3 {
		t.Errorf("fake got query=%q count=%d", fs.query, fs.count)
	}
	data, _ := res.Data.(map[string]any)
	if data["query"] != "今天天气" {
		t.Errorf("data.query = %v", data["query"])
	}
	if _, ok := data["results"].([]extsvc.SearchResult); !ok {
		t.Errorf("data.results type = %T", data["results"])
	}
}

func TestWebSearchToolDefaultCount(t *testing.T) {
	fs := &fakeSearch{}
	res, err := (webSearchTool{}).Execute(context.Background(), json.RawMessage(`{"query":"新闻"}`), webSearchTC(fs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || fs.count != 8 {
		t.Errorf("status=%s count=%d, want ok/8", res.Status, fs.count)
	}
}

func TestWebSearchToolCountClamped(t *testing.T) {
	fs := &fakeSearch{}
	res, err := (webSearchTool{}).Execute(context.Background(), json.RawMessage(`{"query":"新闻","count":99}`), webSearchTC(fs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || fs.count != 10 {
		t.Errorf("status=%s count=%d, want ok/10", res.Status, fs.count)
	}
}

func TestWebSearchToolEmptyQuery(t *testing.T) {
	fs := &fakeSearch{}
	res, err := (webSearchTool{}).Execute(context.Background(), json.RawMessage(`{"query":"   "}`), webSearchTC(fs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusError {
		t.Errorf("status = %s, want error", res.Status)
	}
	if fs.query != "" {
		t.Error("fake should not be called on empty query")
	}
}

func TestWebSearchToolQueryTooLong(t *testing.T) {
	long := make([]byte, 501)
	for i := range long {
		long[i] = 'a'
	}
	args, _ := json.Marshal(map[string]string{"query": string(long)})
	res, err := (webSearchTool{}).Execute(context.Background(), args, webSearchTC(&fakeSearch{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusError {
		t.Errorf("status = %s, want error", res.Status)
	}
}

func TestWebSearchToolServiceError(t *testing.T) {
	fs := &fakeSearch{err: errors.New("firecrawl: api success=false")}
	res, err := (webSearchTool{}).Execute(context.Background(), json.RawMessage(`{"query":"q"}`), webSearchTC(fs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusError {
		t.Errorf("status = %s, want error", res.Status)
	}
}
