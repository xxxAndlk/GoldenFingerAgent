package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"goldenfinger/agent/internal/store"
)

// recordingLongTerm 记录写入与遗忘请求，用来断言共享记忆的来源 ID 语义。
type recordingLongTerm struct {
	appended  []Episode
	forgot    []ForgetRequest
	appendErr error
	forgetErr error
}

func (r *recordingLongTerm) Append(_ context.Context, episode Episode) error {
	if r.appendErr != nil {
		return r.appendErr
	}
	r.appended = append(r.appended, episode)
	return nil
}

func (r *recordingLongTerm) Search(context.Context, MemoryQuery) ([]MemoryFact, error) {
	return nil, nil
}

func (r *recordingLongTerm) Forget(_ context.Context, request ForgetRequest) error {
	if r.forgetErr != nil {
		return r.forgetErr
	}
	r.forgot = append(r.forgot, request)
	return nil
}

func (r *recordingLongTerm) Health(context.Context) (MemoryHealth, error) {
	return MemoryHealth{Ready: true, Backend: "recording"}, nil
}

func sharedService(t *testing.T) (*store.Repos, *Service, *recordingLongTerm, string) {
	t.Helper()
	repos, svc := testRepos(t)
	svc.SetSharedSlots(repos.SharedSlots)
	recorder := &recordingLongTerm{}
	svc.SetLongTermMemory(recorder)
	u := &store.User{UserType: store.UserGeneral, Name: "共享记忆主人", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return repos, svc, recorder, u.ID
}

func TestSaveSharedUsesContentAddressedSource(t *testing.T) {
	_, svc, recorder, ownerID := sharedService(t)
	ctx := context.Background()

	if err := svc.SaveShared(ctx, SharedInput{
		OwnerUserID: ownerID, Slot: BriefSlot("2026-11-05"),
		Text: "工作简报 2026-11-05（第 1 版）\n今天完成了简报接口", Source: SharedSlotBrief,
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.appended) != 1 {
		t.Fatalf("appended=%d", len(recorder.appended))
	}
	sourceID := recorder.appended[0].SourceID
	if !strings.HasPrefix(sourceID, BriefSlot("2026-11-05")+":") {
		t.Fatalf("source_id=%q", sourceID)
	}

	// 同样的内容再写一次：来源 ID 必须相同，云端按 ID 去重才不会出两条。
	if err := svc.SaveShared(ctx, SharedInput{
		OwnerUserID: ownerID, Slot: BriefSlot("2026-11-05"),
		Text: "工作简报 2026-11-05（第 1 版）\n今天完成了简报接口", Source: SharedSlotBrief,
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.appended) != 2 || recorder.appended[1].SourceID != sourceID {
		t.Fatalf("同样的内容应复用同一个来源 ID：%+v", recorder.appended)
	}
	if len(recorder.forgot) != 0 {
		t.Fatalf("内容没变不该删旧版本：%+v", recorder.forgot)
	}
}

func TestSaveSharedCarriesSlotAndWriteTime(t *testing.T) {
	_, svc, recorder, ownerID := sharedService(t)
	ctx := context.Background()

	if err := svc.SaveShared(ctx, SharedInput{
		OwnerUserID: ownerID, Slot: BriefSlot("2026-11-05"), Text: "第一版", Source: SharedSlotBrief,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveShared(ctx, SharedInput{
		OwnerUserID: ownerID, Slot: BriefSlot("2026-11-05"), Text: "第二版", Source: SharedSlotBrief,
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.appended) != 2 {
		t.Fatalf("appended=%d", len(recorder.appended))
	}
	first, second := recorder.appended[0], recorder.appended[1]
	// 槽位要带上：后端据此保证「同一位置只保留最新版」。
	if first.Slot != BriefSlot("2026-11-05") || second.Slot != BriefSlot("2026-11-05") {
		t.Fatalf("slot=%q/%q", first.Slot, second.Slot)
	}
	// 写入时刻要带上：两端交替修改时靠它判断谁更新。
	if first.UpdatedAt.IsZero() || second.UpdatedAt.IsZero() {
		t.Fatalf("updated_at 缺失：%v/%v", first.UpdatedAt, second.UpdatedAt)
	}
	// 内容变了必须换一个来源 ID：云端不允许同 ID 改内容。
	if first.SourceID == second.SourceID {
		t.Fatalf("新内容必须换来源 ID：%q", first.SourceID)
	}
}

func TestForgetSharedForgetsByPrefix(t *testing.T) {
	_, svc, recorder, ownerID := sharedService(t)
	ctx := context.Background()

	slot := PersonNotesSlot("张阿姨")
	if err := svc.SaveShared(ctx, SharedInput{
		OwnerUserID: ownerID, Slot: slot, Text: "张阿姨 的备注：喜欢喝淡茶", Source: SharedSlotPersonNotes,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ForgetShared(ctx, ownerID, slot); err != nil {
		t.Fatal(err)
	}
	if len(recorder.forgot) != 1 {
		t.Fatalf("forgot=%+v", recorder.forgot)
	}
	// 必须按前缀删：另一端写的版本本机并不知道。
	if recorder.forgot[0].SourcePrefix != slot+":" {
		t.Fatalf("forget=%+v", recorder.forgot[0])
	}
}

func TestUpdateNotesWritesSharedMemory(t *testing.T) {
	repos, svc, recorder, _ := sharedService(t)
	ctx := context.Background()
	u := &store.User{UserType: store.UserGeneral, Name: "备注主人", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	person := &store.Person{OwnerUserID: u.ID, CanonicalName: "张阿姨"}
	if err := repos.Persons.Create(ctx, person); err != nil {
		t.Fatal(err)
	}

	if err := svc.UpdateNotes(ctx, u.ID, person.ID, "喜欢喝淡茶"); err != nil {
		t.Fatal(err)
	}
	if len(recorder.appended) != 1 {
		t.Fatalf("appended=%d", len(recorder.appended))
	}
	entry := recorder.appended[0]
	if !strings.HasPrefix(entry.SourceID, PersonNotesSlot("张阿姨")+":") {
		t.Fatalf("source_id=%q", entry.SourceID)
	}
	if !strings.Contains(entry.Text, "张阿姨") || !strings.Contains(entry.Text, "喜欢喝淡茶") {
		t.Fatalf("text=%q", entry.Text)
	}

	// 改备注：写入带上同一个槽位，由后端把旧版本替换掉。
	if err := svc.UpdateNotes(ctx, u.ID, person.ID, "喜欢喝浓茶"); err != nil {
		t.Fatal(err)
	}
	if len(recorder.appended) != 2 {
		t.Fatalf("appended=%d", len(recorder.appended))
	}
	updated := recorder.appended[1]
	if updated.Slot != entry.Slot {
		t.Fatalf("slot=%q want %q", updated.Slot, entry.Slot)
	}
	if updated.SourceID == entry.SourceID {
		t.Fatal("改备注必须换来源 ID")
	}
}

func TestForgetPersonRemovesSharedNotes(t *testing.T) {
	repos, svc, recorder, _ := sharedService(t)
	ctx := context.Background()
	u := &store.User{UserType: store.UserGeneral, Name: "遗忘主人", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	person := &store.Person{OwnerUserID: u.ID, CanonicalName: "李阿姨"}
	if err := repos.Persons.Create(ctx, person); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateNotes(ctx, u.ID, person.ID, "周末不接电话"); err != nil {
		t.Fatal(err)
	}
	recorder.forgot = nil

	if err := svc.ForgetPerson(ctx, u.ID, person.ID); err != nil {
		t.Fatal(err)
	}
	if len(recorder.forgot) != 1 {
		t.Fatalf("forgot=%+v", recorder.forgot)
	}
	if recorder.forgot[0].SourcePrefix != PersonNotesSlot("李阿姨")+":" {
		t.Fatalf("forget=%+v", recorder.forgot[0])
	}
}

func TestSaveSharedReportsAppendFailure(t *testing.T) {
	_, svc, recorder, ownerID := sharedService(t)
	failure := errors.New("outbox unavailable")
	recorder.appendErr = failure
	err := svc.SaveShared(context.Background(), SharedInput{
		OwnerUserID: ownerID, Slot: BriefSlot("2026-11-05"), Text: "简报", Source: SharedSlotBrief,
	})
	if !errors.Is(err, failure) {
		t.Fatalf("SaveShared 应返回排队失败，err=%v", err)
	}
}

func TestForgetSharedReportsForgetFailure(t *testing.T) {
	_, svc, recorder, ownerID := sharedService(t)
	failure := errors.New("delete outbox unavailable")
	recorder.forgetErr = failure
	err := svc.ForgetShared(context.Background(), ownerID, PersonNotesSlot("张阿姨"))
	if !errors.Is(err, failure) {
		t.Fatalf("ForgetShared 应返回删除排队失败，err=%v", err)
	}
}

func TestEnsurePersonReportsInitialNotesFailure(t *testing.T) {
	_, svc, recorder, ownerID := sharedService(t)
	failure := errors.New("outbox unavailable")
	recorder.appendErr = failure
	person, err := svc.EnsurePerson(context.Background(), ownerID, "张阿姨", "家人")
	if person == nil || !errors.Is(err, failure) {
		t.Fatalf("person=%+v err=%v", person, err)
	}
}

func TestUpdateNotesReportsFailureAndKeepsLocalValue(t *testing.T) {
	repos, svc, recorder, ownerID := sharedService(t)
	ctx := context.Background()
	person := &store.Person{OwnerUserID: ownerID, CanonicalName: "张阿姨"}
	if err := repos.Persons.Create(ctx, person); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("outbox unavailable")
	recorder.appendErr = failure
	if err := svc.UpdateNotes(ctx, ownerID, person.ID, "喜欢喝淡茶"); !errors.Is(err, failure) {
		t.Fatalf("UpdateNotes 应返回同步失败，err=%v", err)
	}
	stored, err := repos.Persons.Get(ctx, ownerID, person.ID)
	if err != nil || stored.Notes != "喜欢喝淡茶" {
		t.Fatalf("本机备注应保留：person=%+v err=%v", stored, err)
	}
}

func TestSaveFactReportsFailureAndKeepsLocalFact(t *testing.T) {
	repos, svc, recorder, ownerID := sharedService(t)
	ctx := context.Background()
	person := &store.Person{OwnerUserID: ownerID, CanonicalName: "王阿姨"}
	if err := repos.Persons.Create(ctx, person); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("outbox unavailable")
	recorder.appendErr = failure
	outcome, fact, err := svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: ownerID, Actor: "user", PersonID: person.ID, PersonName: person.CanonicalName,
		FactType: "phone", ValueText: "13900000000", Confidence: 0.95,
	})
	if outcome != OutcomeWritten || fact == nil || !errors.Is(err, failure) {
		t.Fatalf("outcome=%v fact=%+v err=%v", outcome, fact, err)
	}
	if _, err := repos.Facts.Get(ctx, fact.ID); err != nil {
		t.Fatalf("本机事实应保留：%v", err)
	}
}

func TestForgetFactReportsFailureAfterLocalDelete(t *testing.T) {
	repos, svc, recorder, ownerID := sharedService(t)
	ctx := context.Background()
	person := &store.Person{OwnerUserID: ownerID, CanonicalName: "王阿姨"}
	if err := repos.Persons.Create(ctx, person); err != nil {
		t.Fatal(err)
	}
	fact := &store.Fact{PersonID: person.ID, FactType: "phone", ValueText: "13900000000", Status: store.FactConfirmed}
	if err := repos.Facts.Insert(ctx, fact); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("delete outbox unavailable")
	recorder.forgetErr = failure
	if err := svc.ForgetFact(ctx, ownerID, fact.ID); !errors.Is(err, failure) {
		t.Fatalf("ForgetFact 应返回同步失败，err=%v", err)
	}
	if _, err := repos.Facts.Get(ctx, fact.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("本机事实应已删除，err=%v", err)
	}
}

func TestForgetPersonReportsFailureAfterLocalDelete(t *testing.T) {
	repos, svc, recorder, ownerID := sharedService(t)
	ctx := context.Background()
	person := &store.Person{OwnerUserID: ownerID, CanonicalName: "李阿姨"}
	if err := repos.Persons.Create(ctx, person); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("delete outbox unavailable")
	recorder.forgetErr = failure
	if err := svc.ForgetPerson(ctx, ownerID, person.ID); !errors.Is(err, failure) {
		t.Fatalf("ForgetPerson 应返回同步失败，err=%v", err)
	}
	if _, err := repos.Persons.Get(ctx, ownerID, person.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("本机联系人应已遗忘，err=%v", err)
	}
}
