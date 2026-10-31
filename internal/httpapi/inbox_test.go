package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestInboxNilService503(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.Inbox = nil
	h := srv.Handler()
	for _, tc := range [...]struct{ method, path string }{
		{http.MethodGet, "/api/inbox/events"},
		{http.MethodGet, "/api/inbox/search?q=x"},
		{http.MethodGet, "/api/inbox/snooze"},
		{http.MethodGet, "/api/inbox/watches"},
		{http.MethodPost, "/api/inbox/events"},
		{http.MethodPost, "/api/inbox/snooze"},
		{http.MethodPost, "/api/inbox/watches"},
		{http.MethodDelete, "/api/inbox/watches/x"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}

// TestInboxEndpointsContract 按契约逐字段核对八端点（nil embedder → mode=keyword）。
func TestInboxEndpointsContract(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	// 1. POST /api/inbox/events：2 唯一 + 1 重复 → ingested=2。
	rec := postJSON(t, h, "/api/inbox/events", `{"events":[
		{"source_pkg":"com.cainiao","app_name":"菜鸟","title":"包裹到站","text":"取件码 8823-4，请及时领取","posted_at":"2026-10-04T12:00:00+08:00"},
		{"source_pkg":"com.tencent.mm","app_name":"微信","title":"消息","text":"明天下雨记得带伞","posted_at":"2026-10-04T12:01:00+08:00"},
		{"source_pkg":"com.cainiao","app_name":"菜鸟","title":"包裹到站","text":"取件码 8823-4，请及时领取","posted_at":"2026-10-04T12:00:00+08:00"}
	]}`)
	if rec.Code != 200 {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body)
	}
	var ingested struct {
		Ingested int `json:"ingested"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ingested)
	if ingested.Ingested != 2 {
		t.Fatalf("ingested = %d, want 2", ingested.Ingested)
	}

	// 2. GET /api/inbox/events?kind=clue。
	rec = doGet(t, h, "/api/inbox/events?kind=clue")
	var clueResp struct {
		Events []struct {
			ID        string `json:"id"`
			SourcePkg string `json:"source_pkg"`
			AppName   string `json:"app_name"`
			Title     string `json:"title"`
			Text      string `json:"text"`
			Kind      string `json:"kind"`
			ClueType  string `json:"clue_type"`
			ClueValue string `json:"clue_value"`
			PostedAt  string `json:"posted_at"`
		} `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &clueResp)
	if len(clueResp.Events) != 1 {
		t.Fatalf("clue events = %d, want 1: %s", len(clueResp.Events), rec.Body)
	}
	clue := clueResp.Events[0]
	if clue.ClueType != "pickup" || clue.ClueValue != "8823-4" || clue.Kind != "clue" || clue.ID == "" {
		t.Fatalf("clue event = %+v", clue)
	}

	// 3. GET /api/inbox/events（all）。
	rec = doGet(t, h, "/api/inbox/events")
	var allResp struct {
		Events []json.RawMessage `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &allResp)
	if len(allResp.Events) != 2 {
		t.Fatalf("all events = %d, want 2", len(allResp.Events))
	}

	// 4. GET /api/inbox/search?q=雨 → keyword 模式。
	rec = doGet(t, h, "/api/inbox/search?q="+`%E9%9B%A8`)
	var searchResp struct {
		Mode    string `json:"mode"`
		Results []struct {
			ID       string  `json:"id"`
			AppName  string  `json:"app_name"`
			Title    string  `json:"title"`
			Text     string  `json:"text"`
			PostedAt string  `json:"posted_at"`
			Score    float64 `json:"score"`
		} `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &searchResp)
	if searchResp.Mode != "keyword" || len(searchResp.Results) != 1 ||
		searchResp.Results[0].Text != "明天下雨记得带伞" {
		t.Fatalf("search = %+v: %s", searchResp, rec.Body)
	}

	eventID := clue.ID

	// 5. POST /api/inbox/snooze。
	rec = postJSON(t, h, "/api/inbox/snooze", `{"event_id":"`+eventID+`","minutes":10}`)
	var snoozeResp struct {
		ID     string `json:"id"`
		FireAt string `json:"fire_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &snoozeResp)
	if snoozeResp.ID == "" || snoozeResp.FireAt == "" {
		t.Fatalf("snooze resp = %s", rec.Body)
	}

	// 6. GET /api/inbox/snooze。
	rec = doGet(t, h, "/api/inbox/snooze")
	var snoozeList struct {
		Snoozes []struct {
			ID      string `json:"id"`
			EventID string `json:"event_id"`
			Title   string `json:"title"`
			Text    string `json:"text"`
			FireAt  string `json:"fire_at"`
			Status  string `json:"status"`
		} `json:"snoozes"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &snoozeList)
	if len(snoozeList.Snoozes) != 1 || snoozeList.Snoozes[0].EventID != eventID ||
		snoozeList.Snoozes[0].Title != "包裹到站" || snoozeList.Snoozes[0].Status != "pending" {
		t.Fatalf("snooze list = %s", rec.Body)
	}

	// 7. POST /api/inbox/watches。
	rec = postJSON(t, h, "/api/inbox/watches", `{"title":"老王","keyword":"地址","source_pkg":""}`)
	var watchResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &watchResp)
	if watchResp.ID == "" {
		t.Fatalf("watch resp = %s", rec.Body)
	}

	// 8. GET /api/inbox/watches → armed。
	rec = doGet(t, h, "/api/inbox/watches")
	var watchList struct {
		Watches []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Keyword   string `json:"keyword"`
			SourcePkg string `json:"source_pkg"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
		} `json:"watches"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &watchList)
	if len(watchList.Watches) != 1 || watchList.Watches[0].Status != "armed" ||
		watchList.Watches[0].Keyword != "地址" {
		t.Fatalf("watch list = %s", rec.Body)
	}

	// 9. DELETE /api/inbox/watches/{id} → {"ok":true}；重复删除 → 404。
	rec = del(t, h, "/api/inbox/watches/"+watchResp.ID)
	var delResp struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &delResp)
	if rec.Code != 200 || !delResp.OK {
		t.Fatalf("delete watch = %d: %s", rec.Code, rec.Body)
	}
	if rec = del(t, h, "/api/inbox/watches/"+watchResp.ID); rec.Code != 404 {
		t.Fatalf("re-delete = %d, want 404", rec.Code)
	}
}

func del(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestInboxSnoozeByContentMatch 验证手机端可以只上报通知字段（包名/标题/正文），
// 由服务端匹配最新事件后延后提醒——原先这段逻辑写在 Kotlin 的 InboxApi 里。
func TestInboxSnoozeByContentMatch(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	rec := postJSON(t, h, "/api/inbox/events", `{"events":[
		{"source_pkg":"com.cainiao","app_name":"菜鸟","title":"包裹到站","text":"取件码 8823-4，请及时领取","posted_at":"2026-10-04T12:00:00+08:00"}
	]}`)
	if rec.Code != 200 {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body)
	}

	rec = postJSON(t, h, "/api/inbox/snooze",
		`{"source_pkg":"com.cainiao","title":"包裹到站","text":"取件码 8823-4","minutes":30}`)
	if rec.Code != 200 {
		t.Fatalf("match snooze = %d: %s", rec.Code, rec.Body)
	}
	var snoozed struct {
		ID     string `json:"id"`
		FireAt string `json:"fire_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &snoozed)
	if snoozed.ID == "" || snoozed.FireAt == "" {
		t.Fatalf("snooze = %+v", snoozed)
	}

	// 匹配不到时必须如实 404，不能凭空造一条提醒。
	rec = postJSON(t, h, "/api/inbox/snooze",
		`{"source_pkg":"com.cainiao","title":"不存在的标题","minutes":30}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unmatched snooze = %d, want 404", rec.Code)
	}
}
