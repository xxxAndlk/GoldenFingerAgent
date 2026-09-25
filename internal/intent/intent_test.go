package intent

import (
	"context"
	"os"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func TestMatchTrigger(t *testing.T) {
	groups := [][]string{{"张阿姨", "来电话"}, {"张妈", "电话"}}
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"both terms of group 1", "刚才张阿姨来电话了，说晚点到", true},
		{"both terms of group 2", "张妈的电话我没接到", true},
		{"only one term", "张阿姨在楼下", false},
		{"no term", "今天天气不错", false},
		{"empty text", "", false},
		{"case insensitive", "ZHANG Aila来PHONE", false}, // latin terms only match case-insensitively; Chinese unaffected
	}
	for _, c := range cases {
		if got := MatchTrigger(groups, c.text); got != c.want {
			t.Errorf("%s: MatchTrigger(%q) = %v, want %v", c.name, c.text, got, c.want)
		}
	}

	// Latin case-insensitivity.
	if !MatchTrigger([][]string{{"deploy", "rollback"}}, "The DEPLOY needs a ROLLBACK plan") {
		t.Error("latin matching must be case-insensitive")
	}
	// Empty terms are ignored; an empty group never matches.
	if MatchTrigger([][]string{{""}}, "anything") {
		t.Error("empty group must not match")
	}
	if !MatchTrigger([][]string{{"", "吃药"}}, "该吃药了") {
		t.Error("empty terms should be skipped, leaving the real term")
	}
}

// TestServiceLifecycle covers fire → cooldown → re-fire → budget done,
// plus expiry and explicit cancel — all with a controllable clock.
func TestServiceLifecycle(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping intent integration tests")
	}
	ctx := context.Background()
	db, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db.Pool)
	u := &store.User{UserType: store.UserGeneral, Name: "intent-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 5, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	svc := NewService(repos.Intents, repos.Audit, func() time.Time { return now })

	it, err := svc.Create(ctx, CreateInput{
		OwnerUserID:   u.ID,
		Description:   "问她女儿的情况",
		TriggerGroups: [][]string{{"张阿姨", "来电话"}},
		MaxFires:      2,
		Cooldown:      time.Hour,
		ExpiresIn:     24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != store.IntentArmed {
		t.Fatalf("status = %q, want armed", it.Status)
	}

	// No match → nothing.
	if fired, err := svc.Check(ctx, u.ID, "今天吃了吗"); err != nil || len(fired) != 0 {
		t.Fatalf("unexpected fire on non-match: %v %v", fired, err)
	}
	// Match → fire #1.
	fired, err := svc.Check(ctx, u.ID, "张阿姨来电话了")
	if err != nil || len(fired) != 1 {
		t.Fatalf("want 1 fire, got %d (%v)", len(fired), err)
	}
	if fired[0].FireCount != 1 || fired[0].Status != store.IntentFired {
		t.Fatalf("after fire#1: count=%d status=%s", fired[0].FireCount, fired[0].Status)
	}
	// Cooldown: same message right away → nothing.
	if fired, _ := svc.Check(ctx, u.ID, "张阿姨来电话了"); len(fired) != 0 {
		t.Fatalf("cooldown not enforced: %+v", fired)
	}
	// After cooldown → fire #2, budget (2) exhausted → done.
	now = now.Add(2 * time.Hour)
	fired, err = svc.Check(ctx, u.ID, "张阿姨又来电话了")
	if err != nil || len(fired) != 1 {
		t.Fatalf("want re-fire after cooldown, got %d (%v)", len(fired), err)
	}
	if fired[0].Status != store.IntentDone || fired[0].FireCount != 2 {
		t.Fatalf("budget: count=%d status=%s, want 2/done", fired[0].FireCount, fired[0].Status)
	}
	// Done intents never fire again.
	if fired, _ := svc.Check(ctx, u.ID, "张阿姨来电话"); len(fired) != 0 {
		t.Fatalf("done intent fired again: %+v", fired)
	}

	// Expiry: a short-lived intent goes silent and flips to expired.
	it2, err := svc.Create(ctx, CreateInput{
		OwnerUserID:   u.ID,
		Description:   "问体检结果",
		TriggerGroups: [][]string{{"体检"}},
		ExpiresIn:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour) // past it2 expiry
	if fired, _ := svc.Check(ctx, u.ID, "体检报告出来了"); len(fired) != 0 {
		t.Fatalf("expired intent fired: %+v", fired)
	}
	got, err := repos.Intents.Get(ctx, u.ID, it2.ID)
	if err != nil || got.Status != store.IntentExpired {
		t.Fatalf("want expired, got %+v (%v)", got, err)
	}

	// Explicit cancel only.
	it3, err := svc.Create(ctx, CreateInput{
		OwnerUserID:   u.ID,
		Description:   "提醒吃药",
		TriggerGroups: [][]string{{"吃药"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Cancel(ctx, u.ID, it3.ID); err != nil {
		t.Fatal(err)
	}
	if fired, _ := svc.Check(ctx, u.ID, "该吃药了"); len(fired) != 0 {
		t.Fatalf("cancelled intent fired: %+v", fired)
	}

	// Validation: empty description / empty groups rejected.
	if _, err := svc.Create(ctx, CreateInput{OwnerUserID: u.ID, Description: " ", TriggerGroups: [][]string{{"x"}}}); err == nil {
		t.Error("empty description must be rejected")
	}
	if _, err := svc.Create(ctx, CreateInput{OwnerUserID: u.ID, Description: "x", TriggerGroups: nil}); err == nil {
		t.Error("empty groups must be rejected")
	}
}
