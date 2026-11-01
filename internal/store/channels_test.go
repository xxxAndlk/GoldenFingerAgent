package store_test

import (
	"context"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func TestChannelPolicyDefaultsAndSourceOverride(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "channel-policy-user")

	if allowed, err := repos.ChannelPolicies.Allowed(ctx, u.ID, "wechat", "项目群"); err != nil || allowed {
		t.Fatalf("wechat must default deny: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := repos.ChannelPolicies.Allowed(ctx, u.ID, "email", "inbox"); err != nil || !allowed {
		t.Fatalf("email must default allow: allowed=%v err=%v", allowed, err)
	}
	if err := repos.ChannelPolicies.Set(ctx, u.ID, "wechat", "项目群", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if allowed, err := repos.ChannelPolicies.Allowed(ctx, u.ID, "wechat", "项目群"); err != nil || !allowed {
		t.Fatalf("allowed source: allowed=%v err=%v", allowed, err)
	}
	if allowed, _ := repos.ChannelPolicies.Allowed(ctx, u.ID, "wechat", "其他群"); allowed {
		t.Fatal("other sources must stay denied")
	}
	if err := repos.ChannelPolicies.Set(ctx, u.ID, "wechat", "", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := repos.ChannelPolicies.Allowed(ctx, u.ID, "wechat", "其他群"); allowed {
		t.Fatal("default policy must deny")
	}
}

func TestChannelSkipAndDailyBriefVersions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "brief-user")
	now := time.Now()

	if err := repos.ChannelSkips.Increment(ctx, u.ID, "wechat", "2026-11-01", now); err != nil {
		t.Fatal(err)
	}
	if err := repos.ChannelSkips.Increment(ctx, u.ID, "wechat", "2026-11-01", now); err != nil {
		t.Fatal(err)
	}
	skips, err := repos.ChannelSkips.List(ctx, u.ID, "2026-11-01")
	if err != nil || len(skips) != 1 || skips[0].Count != 2 {
		t.Fatalf("skips=%+v err=%v", skips, err)
	}

	first := &store.DailyBrief{OwnerUserID: u.ID, BriefDate: "2026-11-01", ContentMD: "v1",
		Sections: []byte(`{"needs_action":[]}`), SourceRefs: []byte(`["trigger-1"]`)}
	if err := repos.DailyBriefs.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 {
		t.Fatalf("version=%d", first.Version)
	}
	if v, err := repos.DailyBriefs.NextVersion(ctx, u.ID, "2026-11-01"); err != nil || v != 2 {
		t.Fatalf("next version=%d err=%v", v, err)
	}
	second := &store.DailyBrief{OwnerUserID: u.ID, BriefDate: "2026-11-01", Version: 2, ContentMD: "v2"}
	if err := repos.DailyBriefs.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	latest, err := repos.DailyBriefs.Latest(ctx, u.ID, "2026-11-01")
	if err != nil || latest.Version != 2 || latest.ContentMD != "v2" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	changed, err := repos.DailyBriefs.MarkDelivered(ctx, u.ID, latest.ID, now)
	if err != nil || !changed {
		t.Fatalf("deliver: changed=%v err=%v", changed, err)
	}
	latest, _ = repos.DailyBriefs.Latest(ctx, u.ID, "2026-11-01")
	if latest.Status != "delivered" || latest.DeliveredAt == nil {
		t.Fatalf("delivered=%+v", latest)
	}
}
