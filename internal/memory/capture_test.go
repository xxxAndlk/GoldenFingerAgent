package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

func TestCaptureEpisodeCanBeRecalledWithoutEmbedder(t *testing.T) {
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
	u := &store.User{UserType: store.UserGeneral, Name: "capture-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 21, 14, 32, 0, 0, time.FixedZone("CST", 8*3600))
	svc := NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit,
		nil, nlu.Thresholds{FactConfirmed: 0.8}, 720*time.Hour, func() time.Time { return at })

	captured, err := svc.CaptureEpisode(ctx, CaptureInput{
		OwnerUserID:   u.ID,
		Text:          "这个东西很有意思，以后可以研究一下",
		ContextBefore: "我们在看一篇机械键盘的文章",
		ContextAfter:  "它的结构设计很特别",
		Source:        "android_speech", CapturedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured.NeedsContext {
		t.Fatal("context was supplied and must not be marked missing")
	}
	if !strings.Contains(captured.Episode.Summary, "机械键盘") || !strings.Contains(captured.Episode.Summary, "结构设计") {
		t.Fatalf("summary lost context: %q", captured.Episode.Summary)
	}
	var evidence EpisodeEvidence
	if err := json.Unmarshal([]byte(captured.Episode.RawRef), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.OriginalText != "这个东西很有意思，以后可以研究一下" || evidence.ContextBefore == "" {
		t.Fatalf("bad evidence: %+v", evidence)
	}

	results, err := svc.Search(ctx, u.ID, "我前几天说哪个东西很有意思？", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("keyword fallback returned no memory")
	}
	if results[0].ID != captured.Episode.ID || results[0].RawRef == "" || results[0].OccurredAt == nil {
		t.Fatalf("recall did not return source evidence: %+v", results[0])
	}
}

func TestCaptureEpisodeMarksUnresolvedDeixis(t *testing.T) {
	repos, svc := testRepos(t)
	ctx := context.Background()
	u := &store.User{UserType: store.UserGeneral, Name: "deixis-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	result, err := svc.CaptureEpisode(ctx, CaptureInput{OwnerUserID: u.ID, Text: "这个很有意思"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsContext {
		t.Fatal("isolated deictic sentence must be marked as needing context")
	}
	if result.Episode.TimeStart == nil {
		t.Fatal("capture time must be preserved")
	}
}
