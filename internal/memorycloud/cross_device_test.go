package memorycloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

func nluThresholds() nlu.Thresholds {
	return nlu.Thresholds{FactConfirmed: 0.8}
}

// episodeBackend 把云端已收到的片段当作可检索事实返回，
// 用来验证「电脑写入 → 手机检索」这条真实链路。
type episodeBackend struct {
	db *store.DB
}

func (b *episodeBackend) Search(ctx context.Context, query memory.MemoryQuery) ([]memory.MemoryFact, error) {
	episodes, err := store.NewRepos(b.db).LongTerm.ListEpisodes(ctx, query.OwnerID)
	if err != nil {
		return nil, err
	}
	out := make([]memory.MemoryFact, 0, len(episodes))
	for _, ep := range episodes {
		if query.Query != "" && !strings.Contains(ep.Text, query.Query) {
			continue
		}
		out = append(out, memory.MemoryFact{
			ID: ep.SourceID, Text: ep.Text, SourceRefs: []string{ep.SourceID},
		})
	}
	return out, nil
}

func (b *episodeBackend) Forget(context.Context, memory.ForgetRequest) error { return nil }

func (b *episodeBackend) Health(context.Context) (memory.MemoryHealth, error) {
	return memory.MemoryHealth{Ready: true, Backend: "episode-backend"}, nil
}

// crossDevice 装配一端（电脑或手机）的记忆服务。
//
// ownerID 是本机用户 ID：云端的 owner 由设备 Token 决定，
// 两端本地 ID 不同，但写进云端的是同一份长期记忆。
type crossDevice struct {
	svc     *memory.Service
	remote  *memory.Remote
	ownerID string
}

func newCrossDevice(t *testing.T, baseURL, token, deviceID, ownerID, dir string) *crossDevice {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, filepath.Join(dir, "device.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	remote, err := memory.NewRemote(memory.RemoteConfig{
		BaseURL: baseURL, Token: token, DeviceID: deviceID, OwnerID: ownerID, DB: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := memory.NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit,
		nil, nluThresholds(), 720*time.Hour, nil)
	svc.SetSharedSlots(repos.SharedSlots)
	svc.SetLongTermMemory(remote)
	local := &store.User{UserType: store.UserGeneral, Name: deviceID, TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, local); err != nil {
		t.Fatal(err)
	}
	return &crossDevice{svc: svc, remote: remote, ownerID: local.ID}
}

// deviceProxy 是设备与云端之间的代理：可以单独掐断某一台设备的网络。
type deviceProxy struct {
	mu     sync.Mutex
	broken bool
}

func (p *deviceProxy) setBroken(on bool) {
	p.mu.Lock()
	p.broken = on
	p.mu.Unlock()
}

func (p *deviceProxy) isBroken() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.broken
}

// cloudPair 是「一台云端 + 两台设备」的测试环境。
//
// 每台设备各走一层代理：可以只掐断一端的网络，验证断网时的更新与删除
// 会留在本地队列、恢复后补发。
type cloudPair struct {
	desktop    *crossDevice
	phone      *crossDevice
	desktopNet *deviceProxy
	phoneNet   *deviceProxy
}

func newCloudPair(t *testing.T, ownerID string) *cloudPair {
	t.Helper()
	ctx := context.Background()
	cloudDB, err := store.Connect(ctx, filepath.Join(t.TempDir(), "cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cloudDB.Close)
	if _, err := cloudDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cloudRepos := store.NewRepos(cloudDB)
	srv := New(Config{DB: cloudDB, Backend: &episodeBackend{db: cloudDB}})
	cloudTS := httptest.NewServer(srv.Handler())
	t.Cleanup(cloudTS.Close)

	pair := &cloudPair{desktopNet: &deviceProxy{}, phoneNet: &deviceProxy{}}
	desktopProxy := newDeviceProxy(t, cloudTS.URL, pair.desktopNet)
	phoneProxy := newDeviceProxy(t, cloudTS.URL, pair.phoneNet)

	scopes := []string{ScopeMemoryRead, ScopeMemoryWrite}
	desktopToken, _, err := CreateDevice(ctx, cloudRepos, ownerID, "desktop", scopes)
	if err != nil {
		t.Fatal(err)
	}
	phoneToken, _, err := CreateDevice(ctx, cloudRepos, ownerID, "android", scopes)
	if err != nil {
		t.Fatal(err)
	}
	pair.desktop = newCrossDevice(t, desktopProxy, desktopToken, "desktop-1", ownerID, t.TempDir())
	pair.phone = newCrossDevice(t, phoneProxy, phoneToken, "android-1", ownerID, t.TempDir())
	return pair
}

// newDeviceProxy 起一层可掐断的转发代理。
func newDeviceProxy(t *testing.T, upstream string, net *deviceProxy) string {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if net.isBroken() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method,
			upstream+r.URL.RequestURI(), r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL
}

// 电脑写入 → 手机检索；手机写入 → 电脑检索。
// 共享范围只有长期记忆：聊天记录不共享，但这两类内容必须能互相看到。
func TestSharedMemoryCrossDevice(t *testing.T) {
	ctx := context.Background()
	pair := newCloudPair(t, "owner-cross-device")
	desktop, phone := pair.desktop, pair.phone
	ownerID := "owner-cross-device"

	// ---- 电脑写入 → 手机检索 ----
	if err := desktop.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: desktop.ownerID,
		Slot:        memory.BriefSlot("2026-11-05"),
		Text:        "工作简报 2026-11-05（第 1 版）\n今天完成了共享记忆接口",
		Source:      memory.SharedSlotBrief,
	}); err != nil {
		t.Fatal(err)
	}
	if desktop.remote.Flush(ctx) == 0 {
		t.Fatal("电脑端没有把简报上传到云端")
	}
	facts, err := phone.remote.Search(ctx, memory.MemoryQuery{
		OwnerID: ownerID, Query: "简报", Limit: 10,
	})
	if err != nil {
		t.Fatalf("phone search: %v", err)
	}
	if !containsText(facts, "今天完成了共享记忆接口") {
		t.Fatalf("手机端检索不到电脑写的简报：%+v", facts)
	}

	// ---- 手机写入 → 电脑检索 ----
	if err := phone.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: phone.ownerID,
		Slot:        memory.PersonNotesSlot("张阿姨"),
		Text:        "张阿姨 的备注：喜欢喝淡茶",
		Source:      memory.SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	if phone.remote.Flush(ctx) == 0 {
		t.Fatal("手机端没有把备注上传到云端")
	}
	facts, err = desktop.remote.Search(ctx, memory.MemoryQuery{
		OwnerID: ownerID, Query: "张阿姨", Limit: 10,
	})
	if err != nil {
		t.Fatalf("desktop search: %v", err)
	}
	if !containsText(facts, "喜欢喝淡茶") {
		t.Fatalf("电脑端检索不到手机写的备注：%+v", facts)
	}

	// ---- 手机遗忘 → 电脑端也检索不到 ----
	// 遗忘先落本地队列（断网也不会丢），补发之后云端才真的删掉。
	if err := phone.svc.ForgetShared(ctx, phone.ownerID, memory.PersonNotesSlot("张阿姨")); err != nil {
		t.Fatal(err)
	}
	if phone.remote.Flush(ctx) == 0 {
		t.Fatal("手机端没有把遗忘指令发到云端")
	}
	facts, err = desktop.remote.Search(ctx, memory.MemoryQuery{
		OwnerID: ownerID, Query: "张阿姨", Limit: 10,
	})
	if err != nil {
		t.Fatalf("desktop search after forget: %v", err)
	}
	if containsText(facts, "喜欢喝淡茶") {
		t.Fatalf("遗忘后云端仍能召回备注：%+v", facts)
	}
}

// 两端交替修改同一个槽位：云端只保留最新一版，不能新旧两条并存。
func TestSharedMemoryAlternatingUpdatesKeepOneVersion(t *testing.T) {
	ctx := context.Background()
	ownerID := "owner-alternating"
	pair := newCloudPair(t, ownerID)
	desktop, phone := pair.desktop, pair.phone

	slot := memory.PersonNotesSlot("张阿姨")
	if err := desktop.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: desktop.ownerID, Slot: slot,
		Text: "张阿姨 的备注：喜欢喝淡茶", Source: memory.SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	desktop.remote.Flush(ctx)

	// 手机改成另一版：云端应当把旧版替换掉。
	if err := phone.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: phone.ownerID, Slot: slot,
		Text: "张阿姨 的备注：喜欢喝浓茶", Source: memory.SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	phone.remote.Flush(ctx)

	facts, err := desktop.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "张阿姨", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if containsText(facts, "喜欢喝淡茶") {
		t.Fatalf("旧版本没有被替换掉：%+v", facts)
	}
	if !containsText(facts, "喜欢喝浓茶") {
		t.Fatalf("新版本没有生效：%+v", facts)
	}

	// 电脑再改一版：云端同样只保留一版。
	if err := desktop.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: desktop.ownerID, Slot: slot,
		Text: "张阿姨 的备注：喜欢喝红茶", Source: memory.SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	desktop.remote.Flush(ctx)
	facts, err = phone.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "张阿姨", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if containsText(facts, "喜欢喝浓茶") {
		t.Fatalf("交替修改后旧版本仍在：%+v", facts)
	}
	if !containsText(facts, "喜欢喝红茶") {
		t.Fatalf("交替修改后新版本没生效：%+v", facts)
	}
}

// 断网时的更新与删除都要在网络恢复后补发，不能只在本机生效。
func TestSharedMemoryOfflineUpdateAndDeleteRecover(t *testing.T) {
	ctx := context.Background()
	ownerID := "owner-offline"
	pair := newCloudPair(t, ownerID)
	desktop, phone := pair.desktop, pair.phone

	slot := memory.PersonNotesSlot("李阿姨")
	if err := desktop.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: desktop.ownerID, Slot: slot,
		Text: "李阿姨 的备注：周末不接电话", Source: memory.SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	desktop.remote.Flush(ctx)

	// 手机断网：更新与删除都只落在本地队列。
	pair.phoneNet.setBroken(true)
	if err := phone.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: phone.ownerID, Slot: slot,
		Text: "李阿姨 的备注：改到工作日联系", Source: memory.SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	if err := phone.svc.ForgetShared(ctx, phone.ownerID, slot); err != nil {
		t.Fatal(err)
	}
	if sent := phone.remote.Flush(ctx); sent != 0 {
		t.Fatalf("断网时不应有上传成功：%d", sent)
	}
	// 断网期间云端仍是旧内容（删除尚未送达）。
	facts, err := desktop.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "李阿姨", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(facts, "周末不接电话") {
		t.Fatalf("断网期间云端应保留旧内容：%+v", facts)
	}

	// 网络恢复：队列里的删除按退避策略补发（首次失败后要等一会儿），
	// 云端最终不再有这个槽位。
	pair.phoneNet.setBroken(false)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if phone.remote.Flush(ctx) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	facts, err = desktop.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "李阿姨", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if containsText(facts, "李阿姨") {
		t.Fatalf("恢复网络后遗忘没有补发：%+v", facts)
	}
}

func containsText(facts []memory.MemoryFact, needle string) bool {
	for _, fact := range facts {
		if strings.Contains(fact.Text, needle) {
			return true
		}
	}
	return false
}
