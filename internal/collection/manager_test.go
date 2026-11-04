package collection

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
)

func testManager(t *testing.T) (*Manager, *store.Repos, string) {
	t.Helper()
	db, err := store.Connect(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	user := &store.User{UserType: store.UserGeneral, Name: "机主", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	settingsStore, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager := New(repos, settingsStore)
	manager.RequestTimeout = 500 * time.Millisecond
	return manager, repos, user.ID
}

func waitRequest(t *testing.T, manager *Manager, ownerID string) ConnectorRequest {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pending := manager.Pending(ownerID, "")
		if len(pending) == 1 {
			return pending[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("没有等到桌面请求")
	return ConnectorRequest{}
}

func TestWebAddDefaultsToCurrentPageAndCanDisableRemove(t *testing.T) {
	manager, _, ownerID := testManager(t)
	ctx := context.Background()
	outcome, err := manager.Configure(ctx, ownerID, ConfigureInput{Channel: ChannelWeb, Action: "add", URL: "https://example.com/docs/page#part"})
	if err != nil {
		t.Fatal(err)
	}
	data := outcome.Data["site"].(map[string]any)
	siteID := data["id"].(string)
	if data["scope"] != "current_page" || data["max_pages"] != 1 || data["max_depth"] != 0 {
		t.Fatalf("默认范围错误：%v", data)
	}
	site, found := manager.Settings.Site(siteID)
	if !found || site.NavigationEnabled || site.AutoLoginEnabled || len(site.StartURLs) != 1 || site.StartURLs[0] != "https://example.com/docs/page" {
		t.Fatalf("站点配置错误：%+v found=%v", site, found)
	}
	outcome, err = manager.Configure(ctx, ownerID, ConfigureInput{Channel: ChannelWeb, Action: "disable", SiteID: siteID})
	if err != nil {
		t.Fatal(err)
	}
	site, _ = manager.Settings.Site(siteID)
	if site.Enabled {
		t.Fatal("disable 后仍然启用")
	}
	if _, err := manager.Configure(ctx, ownerID, ConfigureInput{Channel: ChannelWeb, Action: "remove", SiteID: siteID}); err != nil {
		t.Fatal(err)
	}
	if _, found := manager.Settings.Site(siteID); found {
		t.Fatal("remove 后站点仍存在")
	}
}

func TestWebRejectsUnsafeURLAndRequiresExplicitSiteScope(t *testing.T) {
	manager, _, ownerID := testManager(t)
	ctx := context.Background()
	for _, input := range []ConfigureInput{
		{Channel: ChannelWeb, Action: "add", URL: "file:///C:/secret.txt"},
		{Channel: ChannelWeb, Action: "add", URL: "https://user:pass@example.com/"},
		{Channel: ChannelWeb, Action: "add", URL: "https://example.com/", IntervalSeconds: 10},
		{Channel: ChannelWeb, Action: "add", URL: "https://example.com/", Scope: "everything"},
	} {
		if _, err := manager.Configure(ctx, ownerID, input); err == nil {
			t.Fatalf("应拒绝：%+v", input)
		}
	}
	outcome, err := manager.Configure(ctx, ownerID, ConfigureInput{Channel: ChannelWeb, Action: "add", URL: "https://example.com/docs", Scope: "site"})
	if err != nil {
		t.Fatal(err)
	}
	site := outcome.Data["site"].(map[string]any)
	if site["scope"] != "site" || site["max_pages"] != 20 || site["max_depth"] != 2 {
		t.Fatalf("整站配置错误：%v", site)
	}
}

func TestWeChatAddUsesUniqueDesktopCandidate(t *testing.T) {
	manager, repos, ownerID := testManager(t)
	type answer struct {
		outcome Outcome
		err     error
	}
	done := make(chan answer, 1)
	go func() {
		outcome, err := manager.Configure(context.Background(), ownerID, ConfigureInput{Channel: ChannelWeChat, Action: "add", Query: "张三"})
		done <- answer{outcome, err}
	}()
	request := waitRequest(t, manager, ownerID)
	if request.Kind != "list_wechat_chats" || request.Query != "张三" {
		t.Fatalf("request=%+v", request)
	}
	chat := WeChatChat{SourceID: "wechat-db:acct:wxid_zhang", ChatKey: "wxid_zhang", DisplayName: "张三", ChatType: "private", AccountID: "acct"}
	if !manager.Complete(ownerID, request.ID, ConnectorResult{OK: true, Chats: []WeChatChat{chat}}) {
		t.Fatal("完成请求失败")
	}
	result := <-done
	if result.err != nil || result.outcome.Question != "" {
		t.Fatalf("outcome=%+v err=%v", result.outcome, result.err)
	}
	allowed, err := repos.ChannelPolicies.Allowed(context.Background(), ownerID, ChannelWeChat, chat.SourceID)
	if err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	enabled, err := repos.ChannelConfigs.Enabled(context.Background(), ownerID, ChannelWeChat)
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	if _, err := manager.Configure(context.Background(), ownerID, ConfigureInput{Channel: ChannelWeChat, Action: "remove", SourceID: chat.SourceID}); err != nil {
		t.Fatal(err)
	}
	allowed, _ = repos.ChannelPolicies.Allowed(context.Background(), ownerID, ChannelWeChat, chat.SourceID)
	if allowed {
		t.Fatal("撤销后仍允许采集")
	}
}

func TestWeChatAmbiguousCandidatesRequireChoice(t *testing.T) {
	manager, repos, ownerID := testManager(t)
	type answer struct {
		outcome Outcome
		err     error
	}
	done := make(chan answer, 1)
	go func() {
		outcome, err := manager.Configure(context.Background(), ownerID, ConfigureInput{Channel: ChannelWeChat, Action: "add", Query: "张三"})
		done <- answer{outcome, err}
	}()
	request := waitRequest(t, manager, ownerID)
	chats := []WeChatChat{
		{SourceID: "wechat-db:acct:one", ChatKey: "one", DisplayName: "张三"},
		{SourceID: "wechat-db:acct:two", ChatKey: "two", DisplayName: "张三"},
	}
	manager.Complete(ownerID, request.ID, ConnectorResult{OK: true, Chats: chats})
	result := <-done
	if result.err != nil || !strings.Contains(result.outcome.Question, "找到 2 个") {
		t.Fatalf("outcome=%+v err=%v", result.outcome, result.err)
	}
	for _, chat := range chats {
		allowed, _ := repos.ChannelPolicies.Allowed(context.Background(), ownerID, ChannelWeChat, chat.SourceID)
		if allowed {
			t.Fatalf("歧义时不应授权：%s", chat.SourceID)
		}
	}
}

func TestActivityChangesOnlyAfterDesktopConfirmation(t *testing.T) {
	manager, repos, ownerID := testManager(t)
	type answer struct {
		outcome Outcome
		err     error
	}
	done := make(chan answer, 1)
	go func() {
		outcome, err := manager.Configure(context.Background(), ownerID, ConfigureInput{Channel: ChannelActivity, Action: "enable"})
		done <- answer{outcome, err}
	}()
	request := waitRequest(t, manager, ownerID)
	if request.Channel != ChannelActivity || request.Kind != "set_activity" || !request.Enabled {
		t.Fatalf("request=%+v", request)
	}
	enabled := true
	manager.Complete(ownerID, request.ID, ConnectorResult{OK: true, Enabled: &enabled})
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	configured, err := repos.ChannelConfigs.Enabled(context.Background(), ownerID, ChannelActivity)
	if err != nil || !configured {
		t.Fatalf("configured=%v err=%v", configured, err)
	}
}

func TestConnectorRequestOwnerIsolationAndTimeout(t *testing.T) {
	manager, _, ownerID := testManager(t)
	manager.RequestTimeout = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := manager.Request(context.Background(), ConnectorRequest{OwnerUserID: ownerID, Channel: ChannelWeChat, Kind: "list_wechat_chats"})
		done <- err
	}()
	request := waitRequest(t, manager, ownerID)
	if manager.Complete("another-owner", request.ID, ConnectorResult{OK: true}) {
		t.Fatal("其他机主不能完成请求")
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "桌面端未响应") {
		t.Fatalf("timeout err=%v", err)
	}
	if len(manager.Pending(ownerID, "")) != 0 {
		t.Fatal("超时请求没有清理")
	}
	if manager.Complete(ownerID, request.ID, ConnectorResult{OK: true}) {
		t.Fatal("超时后不能再完成")
	}
}
