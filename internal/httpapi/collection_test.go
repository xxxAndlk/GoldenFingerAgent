package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
)

func TestCollectionChannelsExposeRealAvailability(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID

	rec := getJSON(t, h, "/api/collection/channels")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	items := decodeMap(t, rec)["channels"].([]any)
	byChannel := map[string]map[string]any{}
	for _, item := range items {
		entry := item.(map[string]any)
		byChannel[entry["channel"].(string)] = entry
	}

	// 手机微信本机可用。
	local := byChannel[store.ChannelWeChatMobile]
	if local == nil {
		t.Fatalf("wechat_mobile missing: %s", rec.Body)
	}
	if local["availability"] != ChannelAvailabilityLocal || local["device"] != "local" {
		t.Fatalf("wechat_mobile=%v", local)
	}
	// 电脑微信必须标明需要电脑在线。
	desktop := byChannel[store.ChannelWeChatDesktop]
	if desktop == nil {
		t.Fatalf("wechat_desktop missing: %s", rec.Body)
	}
	if desktop["availability"] != ChannelAvailabilityDesktop || desktop["device"] != "desktop" {
		t.Fatalf("wechat_desktop=%v", desktop)
	}
	// 尚未接入的能力不能假装可用。
	for _, channel := range []string{store.ChannelFeishu, store.ChannelDingTalk, store.ChannelEmail} {
		item := byChannel[channel]
		if item == nil {
			t.Fatalf("%s missing: %s", channel, rec.Body)
		}
		if item["availability"] != ChannelAvailabilityUnsupported || item["device"] != "none" {
			t.Fatalf("%s=%v", channel, item)
		}
	}
	// 每个渠道都要说明它到底会发生什么。
	for channel, item := range byChannel {
		if note, _ := item["note"].(string); note == "" {
			t.Fatalf("%s has no note: %v", channel, item)
		}
	}
	_ = ownerID
}

func TestCollectionChannelsReportConnectorHeartbeat(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID

	if err := repos.ConnectorStatuses.Save(t.Context(), ownerID, store.ChannelWeChatDesktop,
		"running", []byte(`{"chats":3}`), fixedNow); err != nil {
		t.Fatal(err)
	}
	rec := getJSON(t, h, "/api/collection/channels")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	for _, item := range decodeMap(t, rec)["channels"].([]any) {
		entry := item.(map[string]any)
		if entry["channel"] != store.ChannelWeChatDesktop {
			continue
		}
		if entry["connector_state"] != "running" {
			t.Fatalf("connector_state=%v", entry["connector_state"])
		}
		if entry["last_heartbeat"] == nil || entry["last_heartbeat"] == "" {
			t.Fatalf("missing last_heartbeat: %v", entry)
		}
		return
	}
	t.Fatalf("wechat_desktop missing: %s", rec.Body)
}

func TestConfigureCollectionChannelRequiresRealManager(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	// 未装配采集管理时必须如实报错，不能假装保存成功。
	rec := postJSON(t, h, "/api/collection/channels", `{"channel":"wechat_desktop","enabled":true}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestConfigureCollectionChannelAppliesThroughManager(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID

	settingsStore, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv.Settings = settingsStore
	srv.Collection = collection.New(repos, settingsStore)
	rec := postJSON(t, h, "/api/collection/channels", `{"channel":"wechat_desktop","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["applied"] != true {
		t.Fatalf("body=%s", rec.Body)
	}
	enabled, err := repos.ChannelConfigs.Enabled(t.Context(), ownerID, store.ChannelWeChatDesktop)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("渠道开关没有真的写入")
	}
}

func TestConfigureCollectionChannelFailsWithoutDesktopReceipt(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID

	settingsStore, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv.Settings = settingsStore
	manager := collection.New(repos, settingsStore)
	manager.RequestTimeout = 50 * time.Millisecond
	srv.Collection = manager

	// 工作轨迹的采集要等桌面真实回执；桌面没起来时必须如实失败，
	// 不能显示成「已生效」。
	rec := postJSON(t, h, "/api/collection/channels", `{"channel":"activity","enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	enabled, err := repos.ChannelConfigs.Enabled(t.Context(), ownerID, store.ChannelActivity)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("桌面没有回执时不能保存成已开启")
	}
}
