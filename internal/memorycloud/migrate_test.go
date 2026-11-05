package memorycloud

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func seedLegacy(t *testing.T, db *store.DB, owner string) {
	t.Helper()
	ctx := context.Background()
	repos := store.NewRepos(db)
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	if _, err := repos.LongTerm.InsertEpisode(ctx, &store.LongTermEpisode{
		SourceID: "chat:1", OwnerUserID: owner, Text: "以后总结先列结论。", Source: "chat",
		Role: "owner", ReferenceTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.LongTerm.InsertEpisode(ctx, &store.LongTermEpisode{
		SourceID: "chat:2", OwnerUserID: owner, Text: "我住在杭州。", Source: "chat",
		Role: "owner", ReferenceTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.LongTerm.InsertEdge(ctx, &store.LongTermEdge{
		ID: "edge-1", OwnerUserID: owner, Subject: "用户", Relation: "总结偏好",
		Object: "先结论后要点", Fact: "用户希望总结先列结论，再列三个要点。",
		SourceRefs: []string{"chat:1"}, Status: store.MemoryConfirmed,
		Confidence: 0.9, ValidFrom: &now, Embedding: []float32{0.1, 0.2, 0.3},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.LongTerm.InsertEdge(ctx, &store.LongTermEdge{
		ID: "edge-2", OwnerUserID: owner, Subject: "用户", Relation: "居住地",
		Object: "杭州", Fact: "用户住在杭州。",
		SourceRefs: []string{"chat:2"}, Status: store.MemoryInferred,
		Confidence: 0.55, ValidFrom: &now, Embedding: []float32{0.3, 0.2, 0.1},
	}); err != nil {
		t.Fatal(err)
	}
}

func openTestDB(t *testing.T, name string) *store.DB {
	t.Helper()
	db, err := store.Connect(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	local := openTestDB(t, "local.db")
	seedLegacy(t, local, "u1")
	file, err := Export(ctx, local, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Episodes) != 2 || len(file.Edges) != 2 {
		t.Fatalf("exported episodes=%d edges=%d", len(file.Episodes), len(file.Edges))
	}

	cloud := openTestDB(t, "cloud.db")
	stats, err := Import(ctx, cloud, file, 3)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Episodes != 2 || stats.Edges != 2 || stats.VectorsRebuilt {
		t.Fatalf("stats=%+v", stats)
	}
	repos := store.NewRepos(cloud)
	episodes, err := repos.LongTerm.ListEpisodes(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 2 || episodes[0].SourceID != StoredSourceID("u1", "chat:1") {
		t.Fatalf("episodes=%+v", episodes)
	}
	edges, err := repos.LongTerm.Edges(ctx, "u1", 10)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := 0
	for _, edge := range edges {
		if edge.Status == store.MemoryConfirmed {
			confirmed++
			if len(edge.SourceRefs) != 1 || edge.SourceRefs[0] != StoredSourceID("u1", "chat:1") {
				t.Fatalf("source refs not namespaced: %v", edge.SourceRefs)
			}
			if len(edge.Embedding) != 3 {
				t.Fatalf("embedding len=%d", len(edge.Embedding))
			}
		}
	}
	if confirmed != 1 {
		t.Fatalf("confirmed=%d", confirmed)
	}

	if _, err := Import(ctx, cloud, file, 3); !errors.Is(err, ErrCloudNotEmpty) {
		t.Fatalf("second import err=%v, want ErrCloudNotEmpty", err)
	}
}

func TestImportRebuildsVectorsOnDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	local := openTestDB(t, "local.db")
	seedLegacy(t, local, "u1")
	file, err := Export(ctx, local, "u1")
	if err != nil {
		t.Fatal(err)
	}
	cloud := openTestDB(t, "cloud.db")
	stats, err := Import(ctx, cloud, file, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.VectorsRebuilt || stats.Edges != 0 || stats.Episodes != 2 {
		t.Fatalf("stats=%+v", stats)
	}
	repos := store.NewRepos(cloud)
	edges, _ := repos.LongTerm.Edges(ctx, "u1", 10)
	if len(edges) != 0 {
		t.Fatalf("edges should be skipped when rebuilding vectors: %d", len(edges))
	}
	pending, _, _, err := repos.LongTerm.EpisodeCounts(ctx)
	if err != nil || pending != 2 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
}

func TestImportRejectsDanglingSourceRef(t *testing.T) {
	ctx := context.Background()
	cloud := openTestDB(t, "cloud.db")
	file := &ExportFile{
		SchemaVersion: 1, OwnerUserID: "u1",
		Episodes: []ExportEpisode{{SourceID: "chat:1", Text: "x"}},
		Edges:    []ExportEdge{{Fact: "y", SourceRefs: []string{"chat:missing"}}},
	}
	if _, err := Import(ctx, cloud, file, 0); err == nil {
		t.Fatal("expected dangling ref error")
	}
}
