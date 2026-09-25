package memory

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

func testRepos(t *testing.T) (*store.Repos, *Service) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping memory integration tests")
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
	svc := NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit,
		&mock.FixedEmbedder{Dim: 1024}, nlu.Thresholds{FactConfirmed: 0.8}, 720*time.Hour, nil)
	return repos, svc
}

func TestSaveFactConflictResolution(t *testing.T) {
	repos, svc := testRepos(t)
	ctx := context.Background()

	u := &store.User{UserType: store.UserGeneral, Name: "mem-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	// First confirmed fact.
	out, f1, err := svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "李叔",
		FactType: "family", ValueText: "儿子在深圳", Confidence: 0.9,
	})
	if err != nil || out != OutcomeWritten {
		t.Fatalf("first write: %v %v", out, err)
	}

	// Newer confirmed supersedes (old valid_to set, row kept).
	out, f2, err := svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "李叔",
		FactType: "family", ValueText: "儿子在上海", Confidence: 0.9,
	})
	if err != nil || out != OutcomeSuperseded {
		t.Fatalf("supersede: %v %v", out, err)
	}
	if f2.ID == f1.ID {
		t.Fatal("expected a new fact row")
	}

	// Old fact no longer current.
	olds, err := repos.Facts.ListByPerson(ctx, f1.PersonID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range olds {
		if f.ID == f1.ID {
			t.Fatal("old fact must be closed (valid_to set), not current")
		}
	}

	// An inferred fact conflicting with confirmed → OutcomeConflict (never supersedes).
	out, _, err = svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "李叔",
		FactType: "family", ValueText: "儿子在广州", Confidence: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != OutcomeConflict {
		t.Fatalf("inferred vs confirmed = %v, want conflict", out)
	}
}

func TestEvaluativeFactsRejected(t *testing.T) {
	repos, svc := testRepos(t)
	ctx := context.Background()
	u := &store.User{UserType: store.UserGeneral, Name: "eval-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	out, f, err := svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "王婶",
		FactType: "personality", ValueText: "脾气不好", Confidence: 0.99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != OutcomeRejected || f != nil {
		t.Fatalf("evaluative fact must be rejected at any score, got %v", out)
	}
}

func TestChildFactTypeGate(t *testing.T) {
	repos, svc := testRepos(t)
	ctx := context.Background()
	u := &store.User{UserType: store.UserChild, Name: "child-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	out, _, err := svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "同学小明",
		FactType: "health", ValueText: "感冒了", Confidence: 0.9, IsChildUser: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != OutcomeRejected {
		t.Fatalf("child health fact must be rejected, got %v", out)
	}
	// Factual type passes.
	out, _, err = svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "同学小明",
		FactType: "school", ValueText: "在三年二班", Confidence: 0.9, IsChildUser: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out == OutcomeRejected {
		t.Fatal("factual type must be allowed for children")
	}
}

func TestForgetCascade(t *testing.T) {
	repos, svc := testRepos(t)
	ctx := context.Background()
	u := &store.User{UserType: store.UserGeneral, Name: "forget-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	_, f, err := svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "赵叔",
		FactType: "contact", ValueText: "电话139", Confidence: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ForgetPerson(ctx, u.ID, f.PersonID); err != nil {
		t.Fatal(err)
	}
	// Not retrievable afterwards.
	snips, err := svc.Search(ctx, u.ID, "赵叔 电话", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snips {
		if s.ID == f.ID {
			t.Fatal("forgotten fact must not be retrievable")
		}
	}
	var view *PersonView
	view, err = svc.GetPersonView(ctx, u.ID, f.PersonID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("person page must be gone, got %v (view=%v)", err, view)
	}
}

func TestSearchRanking(t *testing.T) {
	repos, svc := testRepos(t)
	ctx := context.Background()
	u := &store.User{UserType: store.UserGeneral, Name: "rank-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.SaveFact(ctx, SaveFactInput{
		OwnerUserID: u.ID, Actor: "agent", PersonName: "自己",
		FactType: "parking", ValueText: "车位在B2", Confidence: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	snips, err := svc.Search(ctx, u.ID, "车位在哪里", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(snips) == 0 {
		t.Fatal("expected retrieval hit")
	}
	if snips[0].Text != "parking: 车位在B2" {
		t.Errorf("top snippet = %q", snips[0].Text)
	}
}
