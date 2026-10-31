package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOfficialSearchAndFetchAPIs(t *testing.T) {
	var searchSeen, fetchSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/tools/search":
			searchSeen = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["query"] != "DeepSeek" || body["count"] != float64(3) || body["need_content"] != false {
				t.Errorf("search body = %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"官方文档","url":"https://example.com","snippet":"摘要","summary":"详细摘要","site_name":"Example","date":"2026-10-29"}]}`))
		case "/v1/tools/fetch":
			fetchSeen = true
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["url"] != "https://example.com/article" {
				t.Errorf("fetch body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"title":"文章","content":"# 正文"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL+"/v1/", "test-key", 8)
	results, err := client.Search(context.Background(), "DeepSeek", 3)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if results[0].Name != "官方文档" || results[0].Summary != "详细摘要" || results[0].SiteName != "Example" {
		t.Fatalf("result = %+v", results[0])
	}
	page, err := client.Fetch(context.Background(), "https://example.com/article")
	if err != nil || page.Title != "文章" || page.Content != "# 正文" || page.URL != "https://example.com/article" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if !searchSeen || !fetchSeen {
		t.Fatalf("searchSeen=%v fetchSeen=%v", searchSeen, fetchSeen)
	}
}

func TestClientValidatesKeyAndFetchURL(t *testing.T) {
	client := New("", "", 8)
	if _, err := client.Search(context.Background(), "test", 1); err == nil {
		t.Fatal("missing API key should fail")
	}
	client.APIKey = "test-key"
	if _, err := client.Fetch(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("non-http URL should fail")
	}
}
