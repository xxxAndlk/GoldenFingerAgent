package memory

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

func memoryGovernanceFixture(t *testing.T) (*Native, *store.Repos, *store.User, context.Context) {
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
	u := &store.User{UserType: store.UserGeneral, Name: "mem-gov", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	native, err := NewNative(NativeConfig{Repos: repos, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	return native, repos, u, ctx
}

func TestInferredDoesNotInvalidateConfirmedFact(t *testing.T) {
	native, repos, u, ctx := memoryGovernanceFixture(t)
	confirmed := &store.LongTermEdge{
		ID: uuid.NewString(), OwnerUserID: u.ID, Subject: "客户A", Relation: "地址",
		Object: "上海", Fact: "客户A住在上海", Status: store.MemoryConfirmed,
		SourceRefs: []string{"s1"}, Confidence: 0.95,
	}
	if err := repos.LongTerm.InsertEdge(ctx, confirmed); err != nil {
		t.Fatal(err)
	}
	ep := store.LongTermEpisode{
		SourceID: "s2", OwnerUserID: u.ID, Text: "客户A说最近搬到了北京",
		ReferenceTime: time.Now(),
	}
	if err := native.applyFact(ctx, ep, extractedFact{
		Subject: "客户A", Relation: "地址", Object: "北京", Fact: "客户A住在北京",
	}, nil); err != nil {
		t.Fatal(err)
	}
	active, err := repos.LongTerm.ActiveEdgesByTriple(ctx, u.ID, "客户A", "地址")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active=%+v", active)
	}
	var oldConfirmed, conflicted *store.LongTermEdge
	for i := range active {
		switch active[i].Object {
		case "上海":
			oldConfirmed = &active[i]
		case "北京":
			conflicted = &active[i]
		}
	}
	if oldConfirmed == nil || oldConfirmed.ValidTo != nil || oldConfirmed.Status != store.MemoryConfirmed {
		t.Fatalf("confirmed fact must survive inferred conflict: %+v", oldConfirmed)
	}
	if conflicted == nil || conflicted.Status != store.MemoryConflicted {
		t.Fatalf("new conflicting fact must be conflicted: %+v", conflicted)
	}

	changed, err := repos.LongTerm.SetEdgeStatus(ctx, u.ID, conflicted.ID, store.MemoryConfirmed,
		conflicted.Version, u.ID, time.Now())
	if err != nil || !changed {
		t.Fatalf("confirm conflicted fact: changed=%v err=%v", changed, err)
	}
	confirmedAfter, err := repos.LongTerm.GetEdge(ctx, u.ID, conflicted.ID)
	if err != nil || confirmedAfter.Status != store.MemoryConfirmed || confirmedAfter.ConfirmedBy != u.ID {
		t.Fatalf("confirmed edge=%+v err=%v", confirmedAfter, err)
	}
}

func TestSameExtractedFactOnlyAddsSourceRef(t *testing.T) {
	native, repos, u, ctx := memoryGovernanceFixture(t)
	first := store.LongTermEpisode{SourceID: "ep-1", OwnerUserID: u.ID, ReferenceTime: time.Now()}
	fact := extractedFact{Subject: "客户A", Relation: "偏好", Object: "邮件", Fact: "客户A偏好邮件沟通"}
	if err := native.applyFact(ctx, first, fact, nil); err != nil {
		t.Fatal(err)
	}
	second := store.LongTermEpisode{SourceID: "ep-2", OwnerUserID: u.ID, ReferenceTime: time.Now()}
	if err := native.applyFact(ctx, second, fact, nil); err != nil {
		t.Fatal(err)
	}
	edges, err := repos.LongTerm.Edges(ctx, u.ID, 10)
	if err != nil || len(edges) != 1 {
		t.Fatalf("edges=%+v err=%v", edges, err)
	}
	if len(edges[0].SourceRefs) != 2 {
		t.Fatalf("source refs=%v", edges[0].SourceRefs)
	}
	if edges[0].Status != store.MemoryInferred {
		t.Fatalf("status=%s", edges[0].Status)
	}
}
