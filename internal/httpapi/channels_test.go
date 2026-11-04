package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func enableChannel(t *testing.T, srv *Server, channel string, flags ...string) {
	t.Helper()
	u, err := srv.resolveUser(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	item := store.ChannelConfig{
		OwnerUserID: u.ID, Channel: channel,
		Enabled: true, SummaryEnabled: true, BriefEnabled: true,
	}
	for _, flag := range flags {
		switch flag {
		case "enabled":
			item.Enabled = true
		case "summary":
			item.SummaryEnabled = true
		case "index":
			item.IndexEnabled = true
		case "brief":
			item.BriefEnabled = true
		case "auto_worker":
			item.AutoWorkerEnabled = true
		}
	}
	if err := srv.Repos.ChannelConfigs.Save(context.Background(), item, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestActivityEpisodeIngestDedupes(t *testing.T) {
	srv, _ := newTestServer(t)
	enableChannel(t, srv, store.ChannelActivity)
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

func TestActivityEpisodeUsesContextThread(t *testing.T) {
	srv, _ := newTestServer(t)
	enableChannel(t, srv, store.ChannelActivity)
	h := srv.Handler()
	body := `{
		"episode_id":"ep-context-1","context_key":"project:GoldenFingerAgent",
		"started_at":"2026-11-01T09:00:00+08:00","ended_at":"2026-11-01T09:12:00+08:00",
		"app":"VS Code","apps":["VS Code","Chrome"],"project_candidates":["GoldenFingerAgent"],
		"title":"GoldenFingerAgent · 修复登录","summary":"定位并修复了登录接口。",
		"status":"completed","evidence_ids":["obs-1"],"association_confidence":0.5
	}`
	response := postJSON(t, h, "/api/channels/activity/episodes", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	thread := decodeMap(t, response)["thread"].(map[string]any)
	if thread["external_id"] != "activity:project:GoldenFingerAgent" {
		t.Fatalf("thread=%v", thread)
	}
}

func TestEmailMessagesShareThread(t *testing.T) {
	srv, _ := newTestServer(t)
	enableChannel(t, srv, store.ChannelEmail)
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

func TestChannelDisabledBlocksBeforePersistence(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	rec := postJSON(t, h, "/api/channels/email/messages", `{
		"account":"me@example.com","message_id":"disabled-1","subject":"不应入库","text":"正文"}`)
	if rec.Code != http.StatusAccepted || !decodeMap(t, rec)["filtered"].(bool) {
		t.Fatalf("disabled=%d %s", rec.Code, rec.Body)
	}
	triggers := getJSON(t, h, "/api/triggers")
	if len(decodeMap(t, triggers)["triggers"].([]any)) != 0 {
		t.Fatalf("disabled channel body persisted: %s", triggers.Body)
	}
	settings := decodeMap(t, getJSON(t, h, "/api/settings/channels"))
	skips := settings["skips_today"].([]any)
	if len(skips) != 1 || skips[0].(map[string]any)["channel"] != store.ChannelEmail {
		t.Fatalf("skips=%v", settings["skips_today"])
	}
}

func TestChannelConfigAPIUpdatesFlags(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	body := `{"enabled":true,"summary_enabled":false,"index_enabled":true,"brief_enabled":false,"auto_worker_enabled":true}`
	req := httptest.NewRequest(http.MethodPut,
		"/api/settings/channels/config/wechat_mobile", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config=%d %s", rec.Code, rec.Body)
	}
	item := decodeMap(t, rec)
	if item["enabled"] != true || item["index_enabled"] != true ||
		item["summary_enabled"] != false || item["brief_enabled"] != false {
		t.Fatalf("config=%s", rec.Body)
	}
	settings := decodeMap(t, getJSON(t, h, "/api/settings/channels"))
	configs := settings["configs"].([]any)
	found := false
	for _, raw := range configs {
		config := raw.(map[string]any)
		if config["channel"] == store.ChannelWeChatMobile {
			found = true
			if config["enabled"] != true || config["auto_worker_enabled"] != true {
				t.Fatalf("settings config=%v", config)
			}
		}
	}
	if !found {
		t.Fatalf("wechat_mobile missing: %s", settings)
	}
}

func TestWeChatVisibleMessageKeepsDirectionAndCompleteness(t *testing.T) {
	srv, _ := newTestServer(t)
	enableChannel(t, srv, store.ChannelWeChatDesktop)
	h := srv.Handler()
	allowReq := httptest.NewRequest(http.MethodPut, "/api/settings/channels",
		strings.NewReader(`{"channel":"wechat","source":"wechat-uia:严胜易","allowed":true}`))
	allowReq.Header.Set("Content-Type", "application/json")
	allow := httptest.NewRecorder()
	h.ServeHTTP(allow, allowReq)
	if allow.Code != http.StatusOK {
		t.Fatalf("allow=%d %s", allow.Code, allow.Body)
	}
	message := `{
		"schema_version":2,"source":"严胜易","source_id":"wechat-uia:严胜易","display_name":"严胜易",
		"account_id":"acct","chat_type":"private","message_id":"wx-visible-1","event_id":"wx-visible-1",
		"sender":"严胜易","sender_id":"wxid_x","actor_role":"contact",
		"direction":"other","message_type":"text","text":"你好",
		"observed_at":"2026-11-03T09:30:02+08:00","occurred_at":"2026-11-03T09:30:00+08:00",
		"content_hash":"abc","completeness":"complete","sync_scope":"local_incremental",
		"shard":"message_0.db","local_id":123,"sort_seq":456
	}`
	accepted := postJSON(t, h, "/api/channels/wechat/messages", message)
	if accepted.Code != http.StatusCreated {
		t.Fatalf("accepted=%d %s", accepted.Code, accepted.Body)
	}
	triggers := decodeMap(t, getJSON(t, h, "/api/triggers"))["triggers"].([]any)
	if len(triggers) != 1 {
		t.Fatalf("triggers=%v", triggers)
	}
	trigger := triggers[0].(map[string]any)
	if trigger["title"] != "严胜易" {
		t.Fatalf("title=%v", trigger["title"])
	}
	if body, _ := trigger["body"].(string); strings.Contains(body, "当前可见内容") {
		t.Fatalf("complete message got visible-content note: %v", trigger["body"])
	}
	payload, _ := trigger["payload"].(map[string]any)
	if payload["direction"] != "other" || payload["source_id"] != "wechat-uia:严胜易" ||
		payload["actor_role"] != "contact" || payload["sync_scope"] != "local_incremental" ||
		payload["completeness"] != "complete" {
		t.Fatalf("payload=%v", payload)
	}
	if !strings.HasPrefix(fmt.Sprint(trigger["received_at"]), "2026-11-03T09:30:00") {
		t.Fatalf("received_at=%v", trigger["received_at"])
	}
}

func TestWeChatPolicyDeniesBeforePersistence(t *testing.T) {
	srv, _ := newTestServer(t)
	enableChannel(t, srv, store.ChannelWeChatDesktop)
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
