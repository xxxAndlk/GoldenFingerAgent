package inbox_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/inbox"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
)

var clueNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))

func newSvc(t *testing.T, emb llm.Embedder) (*inbox.Service, *store.Repos) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	return inbox.NewService(repos, emb, func() time.Time { return clueNow }, 1024), repos
}

// stubEmbedder：vecs 覆盖的文本返回定制向量，未覆盖返回 nil（模拟 embed 产物为空）。
type stubEmbedder struct {
	vecs map[string][]float32
	err  error
}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		out[i] = s.vecs[txt]
	}
	return out, nil
}

func TestExtractClue(t *testing.T) {
	cases := []struct {
		name, text, wantType, wantValue string
	}{
		{"取件码", "菜鸟驿站 取件码 8823-4", "pickup", "8823-4"},
		{"取货码", "取货码 5566", "pickup", "5566"},
		{"验证码", "【微信】您的验证码是 748152，5分钟内有效", "code", "748152"},
		{"动态码", "动态码 90321", "code", "90321"},
		{"运单号", "您的运单号 SF123456789012 已发出", "tracking", "SF123456789012"},
		{"快递单号", "快递单号:YT987654321098", "tracking", "YT987654321098"},
		{"无关键词", "今晚一起吃饭吗？", "", ""},
		{"订单号不算线索", "订单号 12345678", "", ""},
		{"验证码位数不足", "验证码 123", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotValue := inbox.ExtractClue(tc.text)
			if gotType != tc.wantType || gotValue != tc.wantValue {
				t.Fatalf("ExtractClue(%q) = (%q,%q), want (%q,%q)",
					tc.text, gotType, gotValue, tc.wantType, tc.wantValue)
			}
		})
	}
}

func TestIngestClueKindAndDedupe(t *testing.T) {
	svc, repos := newSvc(t, nil)
	ctx := context.Background()
	n, err := svc.Ingest(ctx, "u1", []inbox.Input{
		{SourcePkg: "com.cainiao", AppName: "菜鸟", Title: "包裹", Text: "取件码 8823-4", PostedAt: clueNow},
		{SourcePkg: "com.tencent.mm", AppName: "微信", Title: "提醒", Text: "明天下雨", PostedAt: clueNow},
	})
	if err != nil || n != 2 {
		t.Fatalf("ingest = %d, %v; want 2", n, err)
	}
	// 重复上报同一批（同分钟桶）→ 全部去重。
	if n, _ := svc.Ingest(ctx, "u1", []inbox.Input{
		{SourcePkg: "com.cainiao", AppName: "菜鸟", Title: "包裹", Text: "取件码 8823-4", PostedAt: clueNow},
	}); n != 0 {
		t.Fatalf("re-ingest = %d, want 0", n)
	}
	all, err := repos.InboxEvents.List(ctx, "u1", "all", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %d, %v; want 2", len(all), err)
	}
	// posted_at 相同则排序平局，按集合断言 kind。
	kinds := []string{all[0].Kind, all[1].Kind}
	sort.Strings(kinds)
	if kinds[0] != "clue" || kinds[1] != "normal" {
		t.Fatalf("kinds = %v, want [clue normal]", kinds)
	}
	clues, _ := repos.InboxEvents.List(ctx, "u1", "clue", 10)
	if len(clues) != 1 || clues[0].ClueType != "pickup" || clues[0].ClueValue != "8823-4" {
		t.Fatalf("clue event = %+v", clues)
	}
	normals, _ := repos.InboxEvents.List(ctx, "u1", "normal", 10)
	if len(normals) != 1 || normals[0].Kind != "normal" {
		t.Fatalf("normal events = %+v", normals)
	}
}

func TestWatchMatch(t *testing.T) {
	svc, repos := newSvc(t, nil)
	ctx := context.Background()
	var pushed []string
	svc.Notify = func(_ context.Context, _, body string) { pushed = append(pushed, body) }

	wHit, _ := svc.CreateWatch(ctx, "u1", "老王", "", "")
	wMiss, _ := svc.CreateWatch(ctx, "u1", "", "绝不出现", "")
	wPkg, _ := svc.CreateWatch(ctx, "u1", "", "地址", "com.cainiao")

	n, err := svc.Ingest(ctx, "u1", []inbox.Input{
		{SourcePkg: "com.tencent.mm", AppName: "微信", Title: "老王", Text: "地址发你了", PostedAt: clueNow},
	})
	if err != nil || n != 1 {
		t.Fatalf("ingest = %d, %v", n, err)
	}
	watches, _ := repos.InboxWatches.List(ctx, "u1")
	status := map[string]string{}
	for _, w := range watches {
		status[w.ID] = w.Status
	}
	if status[wHit.ID] != "fired" {
		t.Fatalf("hit watch status = %q, want fired", status[wHit.ID])
	}
	if status[wMiss.ID] != "armed" || status[wPkg.ID] != "armed" {
		t.Fatalf("miss/pkg watch should stay armed: %v", status)
	}
	if len(pushed) != 1 || !strings.Contains(pushed[0], "等到了：老王") || !strings.Contains(pushed[0], "微信") {
		t.Fatalf("pushed = %v", pushed)
	}
}

func TestSnoozeDueFires(t *testing.T) {
	svc, repos := newSvc(t, nil)
	ctx := context.Background()
	_, _ = svc.Ingest(ctx, "u1", []inbox.Input{
		{SourcePkg: "com.tencent.mm", AppName: "微信", Title: "老王", Text: "地址发你了", PostedAt: clueNow},
	})
	evs, _ := repos.InboxEvents.List(ctx, "u1", "all", 1)
	sn, err := svc.CreateSnooze(ctx, "u1", evs[0].ID, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sn.FireAt.Equal(clueNow.Add(10 * time.Minute)) {
		t.Fatalf("fire_at = %v, want %v", sn.FireAt, clueNow.Add(10*time.Minute))
	}
	if items, _ := svc.Due(ctx, clueNow.Add(9*time.Minute)); len(items) != 0 {
		t.Fatalf("due too early = %v", items)
	}
	items, err := svc.Due(ctx, clueNow.Add(10*time.Minute))
	if err != nil || len(items) != 1 {
		t.Fatalf("due = %v, %v; want 1", items, err)
	}
	if items[0].OwnerUserID != "u1" || !strings.Contains(items[0].Body, "该看了：微信") {
		t.Fatalf("due item = %+v", items[0])
	}
	// 已置 fired：再次 Due 不返回，仓储状态同步。
	if items, _ := svc.Due(ctx, clueNow.Add(20*time.Minute)); len(items) != 0 {
		t.Fatalf("due again = %v, want 0", items)
	}
	views, _ := repos.InboxSnoozes.ListWithEvent(ctx, "u1")
	if len(views) != 1 || views[0].Status != "fired" {
		t.Fatalf("snooze views = %+v", views)
	}
}

func TestSnoozeExplicitFireAtAndValidation(t *testing.T) {
	svc, repos := newSvc(t, nil)
	ctx := context.Background()
	_, _ = svc.Ingest(ctx, "u1", []inbox.Input{
		{SourcePkg: "p", AppName: "日历", Title: "提醒", Text: "取快递", PostedAt: clueNow},
	})
	evs, _ := repos.InboxEvents.List(ctx, "u1", "all", 1)
	at := clueNow.Add(time.Hour)
	if sn, err := svc.CreateSnooze(ctx, "u1", evs[0].ID, 0, &at); err != nil || !sn.FireAt.Equal(at) {
		t.Fatalf("explicit fire_at: %v %v", sn, err)
	}
	if _, err := svc.CreateSnooze(ctx, "u1", evs[0].ID, 0, nil); err == nil {
		t.Fatal("missing minutes/fire_at should error")
	}
	if _, err := svc.CreateSnooze(ctx, "u1", "nope", 10, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing event err = %v", err)
	}
	_ = repos
}

func TestSearchKeywordWithoutEmbedder(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	_, err := svc.Ingest(ctx, "u1", []inbox.Input{
		{SourcePkg: "a", AppName: "快递", Title: "物流", Text: "快递到驿站了", PostedAt: clueNow},
		{SourcePkg: "b", AppName: "日历", Title: "会议", Text: "明天上午开会", PostedAt: clueNow},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Search(ctx, "u1", "快递", 20)
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "keyword" || len(out.Events) != 1 || out.Events[0].Text != "快递到驿站了" {
		t.Fatalf("search = mode %q, %+v", out.Mode, out.Events)
	}
}

func TestSearchVectorAndFallback(t *testing.T) {
	emb := &stubEmbedder{vecs: map[string][]float32{
		"物流\n快递到驿站了": {1, 0, 0, 0},
		"驿站":         {1, 0, 0, 0},
	}}
	svc, _ := newSvc(t, emb)
	ctx := context.Background()
	if _, err := svc.Ingest(ctx, "u1", []inbox.Input{
		{SourcePkg: "a", AppName: "快递", Title: "物流", Text: "快递到驿站了", PostedAt: clueNow},
		{SourcePkg: "b", AppName: "日历", Title: "会议", Text: "明天上午开会", PostedAt: clueNow},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Search(ctx, "u1", "驿站", 20)
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "vector" || len(out.Events) != 1 || out.Events[0].Score < 0.99 {
		t.Fatalf("vector search = mode %q, %+v", out.Mode, out.Events)
	}
	// embed 报错 → 如实降级 keyword，禁止静默。
	svcErr := &stubEmbedder{err: errors.New("embed down")}
	svc2, _ := newSvc(t, svcErr)
	out2, err := svc2.Search(ctx, "u1", "驿站", 20)
	if err != nil {
		t.Fatal(err)
	}
	if out2.Mode != "keyword" {
		t.Fatalf("fallback mode = %q, want keyword", out2.Mode)
	}
}

func TestWatchValidationAndCancel(t *testing.T) {
	svc, repos := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.CreateWatch(ctx, "u1", " ", "", ""); err == nil {
		t.Fatal("empty title+keyword should error")
	}
	w, err := svc.CreateWatch(ctx, "u1", "", "取件", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.CancelWatch(ctx, "u1", w.ID); err != nil || !ok {
		t.Fatalf("cancel = %v, %v", ok, err)
	}
	if ok, _ := svc.CancelWatch(ctx, "u1", w.ID); ok {
		t.Fatal("second cancel should be false")
	}
	watches, _ := repos.InboxWatches.List(ctx, "u1")
	if len(watches) != 1 || watches[0].Status != "cancelled" {
		t.Fatalf("watches = %+v", watches)
	}
}
