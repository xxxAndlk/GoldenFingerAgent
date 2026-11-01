package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActivityEpisodeIngestDedupes(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	body := `{"episode_id":"ep-1","started_at":"2026-11-01T09:00:00+08:00","ended_at":"2026-11-01T09:12:00+08:00","app":"VS Code","title":"修复登录","summary":"定位并修复了登录接口的空指针，提交了改动。","status":"completed","outputs":["login.go"],"evidence_ids":["obs-1"]}`
	first := postJSON(t, h, "/api/channels/activity/episodes", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body)
	}
	created := decodeMap(t, first)
	thread := created["thread"].(map[string]any)
	if thread["channel"] != "activity" {
		t.Fatalf("thread=%v", thread)
	}
	second := postJSON(t, h, "/api/channels/activity/episodes", body)
	if second.Code != http.StatusOK || decodeMap(t, second)["created"].(bool) {
		t.Fatalf("duplicate status=%d body=%s", second.Code, second.Body)
	}
}

func TestEmailMessagesShareThread(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	first := postJSON(t, h, "/api/channels/email/messages", `{
		"account":"me@example.com","mailbox":"INBOX","message_id":"m1","thread_key":"quote",
		"from":"client@example.com","to":["me@example.com"],"subject":"Q4 报价",
		"text":"请在周五前提交 Q4 报价单","received_at":"2026-11-01T10:00:00+08:00"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first=%d %s", first.Code, first.Body)
	}
	threadID := decodeMap(t, first)["thread"].(map[string]any)["id"]
	second := postJSON(t, h, "/api/channels/email/messages", `{
		"account":"me@example.com","mailbox":"INBOX","message_id":"m2","thread_key":"quote",
		"from":"client@example.com","to":["me@example.com"],"subject":"Re: Q4 报价",
		"text":"另外请附上税率","received_at":"2026-11-01T11:00:00+08:00"}`)
	if second.Code != http.StatusCreated {
		t.Fatalf("second=%d %s", second.Code, second.Body)
	}
	if got := decodeMap(t, second)["thread"].(map[string]any)["id"]; got != threadID {
		t.Fatalf("thread=%v want %v", got, threadID)
	}
}

func TestCalendarEventIngest(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	rec := postJSON(t, h, "/api/channels/calendar/events", `{
		"calendar":"工作","event_id":"cal-1","series_id":"weekly-review","title":"产品评审",
		"description":"评审 Q4 路线图","location":"B201","start_at":"2026-11-04T15:00:00+08:00",
		"end_at":"2026-11-04T16:00:00+08:00","attendees":["产品负责人","研发负责人"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("calendar=%d %s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["thread"].(map[string]any)["channel"] != "calendar" {
		t.Fatalf("thread=%v", body["thread"])
	}
	if !strings.Contains(body["trigger"].(map[string]any)["body"].(string), "B201") {
		t.Fatalf("body=%v", body["trigger"])
	}
}

func TestWeChatPolicyDeniesBeforePersistence(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	message := `{"source":"项目群","message_id":"wx1","sender":"张总","text":"周五发版","received_at":"2026-11-01T10:00:00+08:00"}`
	denied := postJSON(t, h, "/api/channels/wechat/messages", message)
	if denied.Code != http.StatusAccepted || !decodeMap(t, denied)["filtered"].(bool) {
		t.Fatalf("denied=%d %s", denied.Code, denied.Body)
	}
	triggers := getJSON(t, h, "/api/triggers")
	if len(decodeMap(t, triggers)["triggers"].([]any)) != 0 {
		t.Fatalf("denied content was persisted: %s", triggers.Body)
	}
	settings := getJSON(t, h, "/api/settings/channels")
	body := decodeMap(t, settings)
	skips := body["skips_today"].([]any)
	if len(skips) != 1 || skips[0].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("skips=%s", settings.Body)
	}

	allowReq := httptest.NewRequest(http.MethodPut, "/api/settings/channels",
		strings.NewReader(`{"channel":"wechat","source":"项目群","allowed":true}`))
	allowReq.Header.Set("Content-Type", "application/json")
	allow := httptest.NewRecorder()
	h.ServeHTTP(allow, allowReq)
	if allow.Code != http.StatusOK {
		t.Fatalf("allow=%d %s", allow.Code, allow.Body)
	}
	accepted := postJSON(t, h, "/api/channels/wechat/messages", message)
	if accepted.Code != http.StatusCreated {
		t.Fatalf("accepted=%d %s", accepted.Code, accepted.Body)
	}
	triggers = getJSON(t, h, "/api/triggers")
	if len(decodeMap(t, triggers)["triggers"].([]any)) != 1 {
		t.Fatalf("allowed content missing: %s", triggers.Body)
	}
}
