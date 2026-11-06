package memorycloud

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
)

// 云端用真实的 Native 抽取后端：写入 → 抽取 → 派生事实 → 另一端检索。
//
// 前面那条用例用的是「片段直接当事实」的替身，只能证明链路通；
// 这里走真实抽取，验证的是记忆真正变成可检索事实之后仍然跨端可用。
func TestSharedMemoryWithNativeExtractionBackend(t *testing.T) {
	ctx := context.Background()
	ownerID := "owner-native"

	cloudDB, err := store.Connect(ctx, filepath.Join(t.TempDir(), "cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cloudDB.Close)
	if _, err := cloudDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cloudRepos := store.NewRepos(cloudDB)
	// 抽取模型：把备注抽成一条事实。
	extractor := mock.New(mock.TextResponse(
		`{"facts":[{"subject":"张阿姨","relation":"备注","object":"喜欢喝淡茶","fact":"张阿姨喜欢喝淡茶"}]}`))
	native, err := memory.NewNative(memory.NativeConfig{
		Repos: cloudRepos, LLM: extractor, Model: "test", Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: cloudDB, Backend: native})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	scopes := []string{ScopeMemoryRead, ScopeMemoryWrite}
	desktopToken, _, err := CreateDevice(ctx, cloudRepos, ownerID, "desktop", scopes)
	if err != nil {
		t.Fatal(err)
	}
	phoneToken, _, err := CreateDevice(ctx, cloudRepos, ownerID, "android", scopes)
	if err != nil {
		t.Fatal(err)
	}
	desktop := newCrossDevice(t, ts.URL, desktopToken, "desktop-1", ownerID, t.TempDir())
	phone := newCrossDevice(t, ts.URL, phoneToken, "android-1", ownerID, t.TempDir())

	// 电脑写入备注并上传。
	if err := desktop.svc.SaveShared(ctx, memory.SharedInput{
		OwnerUserID: desktop.ownerID,
		Slot:        memory.PersonNotesSlot("张阿姨"),
		Text:        "张阿姨 的备注：喜欢喝淡茶",
		Source:      memory.SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	if desktop.remote.Flush(ctx) == 0 {
		t.Fatal("电脑端没有把备注上传到云端")
	}
	// 云端按真实抽取流程生成事实。
	if processed := native.RunOnce(ctx); processed == 0 {
		t.Fatal("云端没有抽取任何片段")
	}

	// 手机从云端检索到的是抽取出来的事实，不是原始片段。
	facts, err := phone.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "张阿姨", Limit: 10})
	if err != nil {
		t.Fatalf("phone search: %v", err)
	}
	if !containsText(facts, "张阿姨喜欢喝淡茶") {
		t.Fatalf("手机端检索不到云端抽取出的事实：%+v", facts)
	}

	// 手机遗忘备注 → 云端派生事实一起删掉，电脑端也检索不到。
	if err := phone.svc.ForgetShared(ctx, phone.ownerID, memory.PersonNotesSlot("张阿姨")); err != nil {
		t.Fatal(err)
	}
	if phone.remote.Flush(ctx) == 0 {
		t.Fatal("手机端没有把遗忘指令发到云端")
	}
	facts, err = desktop.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "张阿姨", Limit: 10})
	if err != nil {
		t.Fatalf("desktop search after forget: %v", err)
	}
	if containsText(facts, "张阿姨") {
		t.Fatalf("遗忘后云端仍能召回事实：%+v", facts)
	}
}

// 单条联系人事实删除后，另一端也不能再检索到。
//
// 事实的长期记忆条目在云端是按来源引用（fact:<id>）关联的，
// 删事实时必须连来源一起删，否则云端派生事实会留着。
func TestSharedMemoryFactDeletionAcrossDevices(t *testing.T) {
	ctx := context.Background()
	ownerID := "owner-fact-delete"
	ts, native, cloudRepos := newNativeCloud(t, ownerID)

	scopes := []string{ScopeMemoryRead, ScopeMemoryWrite}
	desktopToken, _, err := CreateDevice(ctx, cloudRepos, ownerID, "desktop", scopes)
	if err != nil {
		t.Fatal(err)
	}
	phoneToken, _, err := CreateDevice(ctx, cloudRepos, ownerID, "android", scopes)
	if err != nil {
		t.Fatal(err)
	}
	desktop := newCrossDevice(t, ts.URL, desktopToken, "desktop-1", ownerID, t.TempDir())
	phone := newCrossDevice(t, ts.URL, phoneToken, "android-1", ownerID, t.TempDir())

	// 电脑记录一条联系人事实。
	outcome, fact, err := desktop.svc.SaveFact(ctx, memory.SaveFactInput{
		OwnerUserID: desktop.ownerID, Actor: "user", PersonName: "王阿姨",
		FactType: "phone", ValueText: "13900000000", Confidence: 0.95,
	})
	if err != nil || outcome != memory.OutcomeWritten || fact == nil {
		t.Fatalf("outcome=%v fact=%+v err=%v", outcome, fact, err)
	}
	if desktop.remote.Flush(ctx) == 0 {
		t.Fatal("电脑端没有把事实上传到云端")
	}
	native.RunOnce(ctx)

	// 手机能检索到这条事实。
	facts, err := phone.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "王阿姨", Limit: 10})
	if err != nil {
		t.Fatalf("phone search: %v", err)
	}
	if !containsText(facts, "王阿姨") {
		t.Fatalf("手机端检索不到电脑记录的事实：%+v", facts)
	}

	// 电脑删除这条事实。
	if err := desktop.svc.ForgetFact(ctx, desktop.ownerID, fact.ID); err != nil {
		t.Fatal(err)
	}
	if desktop.remote.Flush(ctx) == 0 {
		t.Fatal("电脑端没有把遗忘指令发到云端")
	}
	facts, err = phone.remote.Search(ctx, memory.MemoryQuery{OwnerID: ownerID, Query: "王阿姨", Limit: 10})
	if err != nil {
		t.Fatalf("phone search after forget: %v", err)
	}
	if containsText(facts, "王阿姨") {
		t.Fatalf("删除事实后另一端仍能检索到：%+v", facts)
	}
}

// newNativeCloud 起一台用真实 Native 抽取后端的云端。
func newNativeCloud(t *testing.T, ownerID string) (*httptest.Server, *memory.Native, *store.Repos) {
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
	extractor := mock.New(mock.TextResponse(
		`{"facts":[{"subject":"王阿姨","relation":"电话","object":"13900000000","fact":"王阿姨的电话是13900000000"}]}`))
	native, err := memory.NewNative(memory.NativeConfig{
		Repos: cloudRepos, LLM: extractor, Model: "test", Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: cloudDB, Backend: native})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, native, cloudRepos
}
