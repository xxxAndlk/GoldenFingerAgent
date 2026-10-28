package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCaptureAndSearchEpisodeEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	body := `{"text":"这个东西很有意思","context_before":"一篇机械键盘文章","source":"android_speech","captured_at":"2026-03-01T14:32:00+08:00"}`
	rec := postJSON(t, h, "/api/memory/capture", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("capture status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "机械键盘") {
		t.Fatalf("capture response lost context: %s", rec.Body)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/memory/search?q="+
		"%E6%88%91%E5%89%8D%E5%87%A0%E5%A4%A9%E8%AF%B4%E5%93%AA%E4%B8%AA%E4%B8%9C%E8%A5%BF%E5%BE%88%E6%9C%89%E6%84%8F%E6%80%9D", nil)
	searchRec := httptest.NewRecorder()
	h.ServeHTTP(searchRec, req)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("search status %d: %s", searchRec.Code, searchRec.Body)
	}
	var response struct {
		Results []struct {
			ID     string `json:"id"`
			RawRef string `json:"raw_ref"`
			Text   string `json:"text"`
		} `json:"results"`
	}
	if err := json.Unmarshal(searchRec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].RawRef == "" || !strings.Contains(response.Results[0].Text, "机械键盘") {
		t.Fatalf("unexpected search response: %s", searchRec.Body)
	}
}

func TestCaptureEpisodeRejectsBlankText(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := postJSON(t, srv.Handler(), "/api/memory/capture", `{"text":"  "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
