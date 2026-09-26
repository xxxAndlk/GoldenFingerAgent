package firecrawl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/extsvc"
)

func TestSearchBuildsRequestAndParses(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":[
			{"title":"标题A","url":"https://a.example","description":"摘要A","score":0.9},
			{"title":"标题B","url":"https://b.example","description":"摘要B"}
		]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key", 8)
	res, err := c.Search(context.Background(), "今天天气", 5)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2/search" {
		t.Errorf("URL path = %q, want /v2/search", gotPath)
	}
	if !strings.Contains(gotAuth, "Bearer test-key") {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatal(err)
	}
	if body["query"] != "今天天气" || body["limit"] != float64(5) {
		t.Errorf("body = %v", body)
	}
	if srcs, ok := body["sources"].([]any); !ok || len(srcs) != 1 || srcs[0] != "web" {
		t.Errorf("sources = %v, want [web]", body["sources"])
	}
	want := []extsvc.SearchResult{
		{Name: "标题A", URL: "https://a.example", Snippet: "摘要A"},
		{Name: "标题B", URL: "https://b.example", Snippet: "摘要B"},
	}
	if len(res) != 2 || res[0] != want[0] || res[1] != want[1] {
		t.Errorf("results = %+v, want %+v", res, want)
	}
}

func TestSearchDefaultCount(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", 0) // count 未设置 -> 客户端默认 8
	if _, err := c.Search(context.Background(), "q", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"limit":8`) {
		t.Errorf("body = %s, want limit 8", gotBody)
	}
}

func TestSearchSuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":false,"error":"invalid api key"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "bad", 8).Search(context.Background(), "q", 8)
	if err == nil || !strings.Contains(err.Error(), "success=false") {
		t.Errorf("err = %v, want api success=false", err)
	}
}

func TestSearchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k", 8).Search(context.Background(), "q", 8)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want http 502", err)
	}
}

func TestSearchTransportFailure(t *testing.T) {
	_, err := New("http://127.0.0.1:1", "k", 8).Search(context.Background(), "q", 8)
	if err == nil {
		t.Error("want error on transport failure")
	}
}
