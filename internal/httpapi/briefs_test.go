package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"goldenfinger/agent/internal/memory"
)

func TestBriefEndpointsGenerateLatestAndDeliver(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	generated := postJSON(t, h, "/api/briefs/generate?date=2026-11-01", `{}`)
	if generated.Code != http.StatusCreated {
		t.Fatalf("generate=%d %s", generated.Code, generated.Body)
	}
	brief := decodeMap(t, generated)["brief"].(map[string]any)
	id := brief["id"].(string)
	if brief["brief_date"] != "2026-11-01" || brief["version"].(float64) != 1 {
		t.Fatalf("brief=%v", brief)
	}

	latest := getJSON(t, h, "/api/briefs/latest?date=2026-11-01")
	if latest.Code != http.StatusOK {
		t.Fatalf("latest=%d %s", latest.Code, latest.Body)
	}
	list := getJSON(t, h, "/api/briefs")
	if list.Code != http.StatusOK || len(decodeMap(t, list)["briefs"].([]any)) != 1 {
		t.Fatalf("list=%d %s", list.Code, list.Body)
	}

	delivered := postJSON(t, h, "/api/briefs/"+id+"/delivered", `{}`)
	if delivered.Code != http.StatusOK {
		t.Fatalf("deliver=%d %s", delivered.Code, delivered.Body)
	}
	if decodeMap(t, delivered)["brief"].(map[string]any)["status"] != "delivered" {
		t.Fatalf("delivered=%s", delivered.Body)
	}
	again := postJSON(t, h, "/api/briefs/"+id+"/delivered", `{}`)
	if again.Code != http.StatusConflict {
		t.Fatalf("second deliver=%d %s", again.Code, again.Body)
	}
}

type failingBriefSharedMemory struct{}

func (failingBriefSharedMemory) SaveShared(context.Context, memory.SharedInput) error {
	return errors.New("outbox unavailable")
}

func TestBriefGenerateReturnsBriefAndSharedSyncError(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.Briefs.SharedMemory = failingBriefSharedMemory{}
	rec := postJSON(t, srv.Handler(), "/api/briefs/generate?date=2026-11-01", `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate=%d %s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["brief"].(map[string]any)["id"] == "" {
		t.Fatalf("同步失败仍应返回已生成的简报：%v", body)
	}
	if body["shared_sync_error"] == "" {
		t.Fatalf("必须明确返回共享同步错误：%v", body)
	}
	latest := getJSON(t, srv.Handler(), "/api/briefs/latest?date=2026-11-01")
	if latest.Code != http.StatusOK {
		t.Fatalf("本机简报应已保存：%d %s", latest.Code, latest.Body)
	}
}
