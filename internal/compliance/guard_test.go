package compliance

import (
	"context"
	"errors"
	"os"
	"testing"

	"goldenfinger/agent/internal/store"
)

func testGuard(t *testing.T) (*Guard, *store.Repos) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping compliance integration tests")
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
	return NewGuard(repos.Consents, repos.Audit, repos.Users), repos
}

func TestChildConsentGate(t *testing.T) {
	guard, repos := testGuard(t)
	ctx := context.Background()

	child := &store.User{UserType: store.UserChild, Name: "小孩", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, child); err != nil {
		t.Fatal(err)
	}

	// 无同意 → 被阻止。
	if err := guard.RequireConsent(ctx, child, "basic"); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("want ErrConsentRequired, got %v", err)
	}

	// 监护人授予同意 → 放行。
	var guardianID *string
	if _, err := repos.Consents.Grant(ctx, child.ID, "basic", guardianID); err != nil {
		t.Fatal(err)
	}
	if err := guard.RequireConsent(ctx, child, "basic"); err != nil {
		t.Fatalf("consent granted but blocked: %v", err)
	}

	// 成年人永不需要同意。
	adult := &store.User{UserType: store.UserElder, Name: "老人", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, adult); err != nil {
		t.Fatal(err)
	}
	if err := guard.RequireConsent(ctx, adult, "basic"); err != nil {
		t.Fatalf("adult must pass: %v", err)
	}
}

func TestChildContentFilter(t *testing.T) {
	guard, repos := testGuard(t)
	ctx := context.Background()
	child := &store.User{UserType: store.UserChild, Name: "过滤小孩", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err := guard.FilterContent(ctx, child, "讲个赌博的故事"); !errors.Is(err, ErrContentBlocked) {
		t.Fatalf("unsafe content must be blocked, got %v", err)
	}
	if err := guard.FilterContent(ctx, child, "今天天气真好"); err != nil {
		t.Fatalf("safe content must pass: %v", err)
	}
}

func TestFactTypePolicy(t *testing.T) {
	_, repos := testGuard(t)
	ctx := context.Background()
	guard := NewGuard(repos.Consents, repos.Audit, repos.Users)

	child := &store.User{UserType: store.UserChild, Name: "事实小孩", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	adult := &store.User{UserType: store.UserGeneral, Name: "大人", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, adult); err != nil {
		t.Fatal(err)
	}

	// 评价性事实对所有人一律阻止。
	if err := guard.FactTypeAllowed(adult, "personality", "脾气不好"); !errors.Is(err, ErrContentBlocked) {
		t.Error("evaluative must be blocked for adults too")
	}
	// 儿童：被阻止的事实类型拒绝，事实型放行。
	if err := guard.FactTypeAllowed(child, "health", "感冒"); !errors.Is(err, ErrContentBlocked) {
		t.Error("child health facts must be blocked")
	}
	if err := guard.FactTypeAllowed(child, "school", "三年二班"); err != nil {
		t.Error("child factual facts must pass")
	}
}
