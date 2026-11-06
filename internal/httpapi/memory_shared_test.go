package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
)

// sharedMemoryRecorder 记录写入长期记忆的内容与遗忘请求。
type sharedMemoryRecorder struct {
	appended  []memory.Episode
	forgot    []memory.ForgetRequest
	appendErr error
	forgetErr error
}

func (r *sharedMemoryRecorder) Append(_ context.Context, episode memory.Episode) error {
	if r.appendErr != nil {
		return r.appendErr
	}
	r.appended = append(r.appended, episode)
	return nil
}

func (r *sharedMemoryRecorder) Search(context.Context, memory.MemoryQuery) ([]memory.MemoryFact, error) {
	return nil, nil
}

func (r *sharedMemoryRecorder) Forget(_ context.Context, request memory.ForgetRequest) error {
	if r.forgetErr != nil {
		return r.forgetErr
	}
	r.forgot = append(r.forgot, request)
	return nil
}

func (r *sharedMemoryRecorder) Health(context.Context) (memory.MemoryHealth, error) {
	return memory.MemoryHealth{Ready: true, Backend: "recording"}, nil
}

func createPerson(t *testing.T, repos *store.Repos, ownerID, name string) *store.Person {
	t.Helper()
	person := &store.Person{OwnerUserID: ownerID, CanonicalName: name}
	if err := repos.Persons.Create(context.Background(), person); err != nil {
		t.Fatal(err)
	}
	return person
}

// 联系人备注必须进共享长期记忆：手机与电脑只共享这一份记忆。
func TestPersonNotesReachSharedMemory(t *testing.T) {
	srv, repos := newTestServer(t)
	recorder := &sharedMemoryRecorder{}
	srv.Memory.SetLongTermMemory(recorder)
	h := srv.Handler()

	owner := ownerUser(t, repos, "主人")
	person := createPerson(t, repos, owner.ID, "张阿姨")

	req := httptest.NewRequest(http.MethodPut, "/api/persons/"+person.ID,
		strings.NewReader(`{"notes":"喜欢喝淡茶"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if len(recorder.appended) != 1 {
		t.Fatalf("appended=%d", len(recorder.appended))
	}
	entry := recorder.appended[0]
	if !strings.HasPrefix(entry.SourceID, memory.PersonNotesSlot("张阿姨")+":") {
		t.Fatalf("source_id=%q", entry.SourceID)
	}
	if !strings.Contains(entry.Text, "喜欢喝淡茶") {
		t.Fatalf("text=%q", entry.Text)
	}

	// 遗忘联系人：共享长期记忆里的备注也要删掉，
	// 否则「忘了这个人」只是本机删了，另一端仍能召回。
	recorder.forgot = nil
	req = httptest.NewRequest(http.MethodDelete, "/api/persons/"+person.ID, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("forget status=%d body=%s", rec.Code, rec.Body)
	}
	if len(recorder.forgot) != 1 {
		t.Fatalf("forgot=%+v", recorder.forgot)
	}
	if recorder.forgot[0].SourcePrefix != memory.PersonNotesSlot("张阿姨")+":" {
		t.Fatalf("forget=%+v", recorder.forgot[0])
	}
}

// 共享记忆端点供桌面写入协同总结等跨端内容。
func TestSharedMemoryEndpointWritesSlot(t *testing.T) {
	srv, _ := newTestServer(t)
	recorder := &sharedMemoryRecorder{}
	srv.Memory.SetLongTermMemory(recorder)
	h := srv.Handler()

	rec := postJSON(t, h, "/api/memory/shared",
		`{"slot":"collab_summary:phase-1","text":"协作阶段总结：完成了简报接口","source":"collab_summary"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if len(recorder.appended) != 1 {
		t.Fatalf("appended=%d", len(recorder.appended))
	}
	if recorder.appended[0].SourceID != memory.SharedSourceID("collab_summary:phase-1", "协作阶段总结：完成了简报接口") {
		t.Fatalf("source_id=%q", recorder.appended[0].SourceID)
	}
	if recorder.appended[0].Text != "协作阶段总结：完成了简报接口" {
		t.Fatalf("text=%q", recorder.appended[0].Text)
	}

	// 缺少槽位必须被拒绝：没有槽位就没法做更新与遗忘。
	rec = postJSON(t, h, "/api/memory/shared", `{"text":"没有槽位"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing slot status=%d body=%s", rec.Code, rec.Body)
	}
}

// 单条事实删除要走记忆服务：owner 归属校验 + 云端派生记忆一起删。
func TestForgetFactReachesSharedMemory(t *testing.T) {
	srv, repos := newTestServer(t)
	recorder := &sharedMemoryRecorder{}
	srv.Memory.SetLongTermMemory(recorder)
	h := srv.Handler()
	ctx := context.Background()

	owner := ownerUser(t, repos, "主人")
	other := ownerUser(t, repos, "别人")
	person := &store.Person{OwnerUserID: owner.ID, CanonicalName: "王阿姨"}
	if err := repos.Persons.Create(ctx, person); err != nil {
		t.Fatal(err)
	}
	fact := &store.Fact{
		PersonID: person.ID, FactType: "phone", ValueText: "13900000000",
		Confidence: 0.95, Status: store.FactConfirmed, ValidFrom: fixedNow,
	}
	if err := repos.Facts.Insert(ctx, fact); err != nil {
		t.Fatal(err)
	}

	// 别人的用户不能删这条事实。
	req := httptest.NewRequest(http.MethodDelete, "/api/facts/"+fact.ID, nil)
	req.Header.Set("X-User-Id", other.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner delete status=%d body=%s", rec.Code, rec.Body)
	}
	if _, err := repos.Facts.Get(ctx, fact.ID); err != nil {
		t.Fatalf("别人的请求不该删掉事实: %v", err)
	}

	// 本人删除：云端派生记忆按来源引用一起删。
	req = httptest.NewRequest(http.MethodDelete, "/api/facts/"+fact.ID, nil)
	req.Header.Set("X-User-Id", owner.ID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body)
	}
	if len(recorder.forgot) != 1 {
		t.Fatalf("forgot=%+v", recorder.forgot)
	}
	if recorder.forgot[0].SourceID != "fact:"+fact.ID {
		t.Fatalf("forget source=%q want fact:%s", recorder.forgot[0].SourceID, fact.ID)
	}
	if recorder.forgot[0].OwnerID != owner.ID {
		t.Fatalf("forget owner=%q", recorder.forgot[0].OwnerID)
	}
}

func TestSharedMemoryEndpointReportsQueueFailure(t *testing.T) {
	srv, _ := newTestServer(t)
	recorder := &sharedMemoryRecorder{appendErr: errors.New("outbox unavailable")}
	srv.Memory.SetLongTermMemory(recorder)
	rec := postJSON(t, srv.Handler(), "/api/memory/shared",
		`{"slot":"collab_summary:phase-1","text":"协作阶段总结","source":"collab_summary"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "outbox unavailable") {
		t.Fatalf("同步排队失败不能返回成功：status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestPersonNotesEndpointReportsQueueFailure(t *testing.T) {
	srv, repos := newTestServer(t)
	recorder := &sharedMemoryRecorder{appendErr: errors.New("outbox unavailable")}
	srv.Memory.SetLongTermMemory(recorder)
	owner := ownerUser(t, repos, "主人")
	person := createPerson(t, repos, owner.ID, "张阿姨")
	req := httptest.NewRequest(http.MethodPut, "/api/persons/"+person.ID, strings.NewReader(`{"notes":"喜欢喝淡茶"}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "本机保存") {
		t.Fatalf("备注同步失败响应不明确：status=%d body=%s", rec.Code, rec.Body)
	}
	stored, err := repos.Persons.Get(context.Background(), owner.ID, person.ID)
	if err != nil || stored.Notes != "喜欢喝淡茶" {
		t.Fatalf("本机备注应保留：person=%+v err=%v", stored, err)
	}
}

func TestForgetFactEndpointReportsQueueFailure(t *testing.T) {
	srv, repos := newTestServer(t)
	recorder := &sharedMemoryRecorder{forgetErr: errors.New("delete outbox unavailable")}
	srv.Memory.SetLongTermMemory(recorder)
	ctx := context.Background()
	owner := ownerUser(t, repos, "主人")
	person := createPerson(t, repos, owner.ID, "王阿姨")
	fact := &store.Fact{PersonID: person.ID, FactType: "phone", ValueText: "13900000000", Status: store.FactConfirmed}
	if err := repos.Facts.Insert(ctx, fact); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/facts/"+fact.ID, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "本机删除") {
		t.Fatalf("事实同步删除失败响应不明确：status=%d body=%s", rec.Code, rec.Body)
	}
	if _, err := repos.Facts.Get(ctx, fact.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("本机事实应已删除，err=%v", err)
	}
}
