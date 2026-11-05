package store_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/migrations"
)

// applyLegacySchema 只应用给定前缀的迁移，用于构造“升级前的旧库”。
func applyLegacySchema(t *testing.T, db *store.DB, names ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		raw, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, stmt := range strings.Split(string(raw), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("apply %s: %v\n%s", name, err, stmt)
			}
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestUpgradeFromLegacySchema 验证只跑过 0001-0004 的旧库能平滑升到长期记忆表，
// 且不会重跑已应用的迁移、不会丢已有数据。
func TestUpgradeFromLegacySchema(t *testing.T) {
	ctx := context.Background()
	db, err := store.Connect(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	applyLegacySchema(t, db,
		"0001_init.sql", "0002_indexes.sql", "0003_intents.sql", "0004_inbox.sql")

	repos := store.NewRepos(db)
	u := &store.User{UserType: store.UserGeneral, Name: "旧库用户", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	applied, err := db.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	want := []string{"0005_agent_jobs.sql", "0006_longterm_memory.sql", "0007_superagent.sql", "0008_single_main_session.sql", "0009_channels_briefs.sql", "0010_channel_configs.sql",
		"0011_work_tasks.sql", "0012_execution_control.sql", "0013_memory_policy_devices.sql",
		"0014_connector_status.sql", "0015_longterm_role.sql", "0016_cloud_memory.sql",
		"0017_memory_outbox.sql", "0018_artifact_files.sql", "0019_delivery.sql",
		"0020_delivery_allow_executable.sql"}
	if strings.Join(applied, ",") != strings.Join(want, ",") {
		t.Fatalf("applied=%v want %v", applied, want)
	}
	// 再跑一次必须是空操作。
	if again, err := db.Migrate(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second migrate applied=%v err=%v", again, err)
	}
	if _, err := repos.Users.Get(ctx, u.ID); err != nil {
		t.Fatalf("existing user lost: %v", err)
	}
}

func TestLongTermRepoRoundTripAndIdempotency(t *testing.T) {
	ctx := context.Background()
	db, err := store.Connect(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repo := store.NewRepos(db).LongTerm
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

	ep := &store.LongTermEpisode{SourceID: "chat:1", OwnerUserID: "u1", Text: "张阿姨的女儿在上海工作",
		Source: "chat", ReferenceTime: at}
	inserted, err := repo.InsertEpisode(ctx, ep)
	if err != nil || !inserted {
		t.Fatalf("insert=%v err=%v", inserted, err)
	}
	inserted, err = repo.InsertEpisode(ctx, ep)
	if err != nil || inserted {
		t.Fatalf("second insert=%v err=%v (must be idempotent)", inserted, err)
	}

	pending, err := repo.PendingEpisodes(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%d err=%v", len(pending), err)
	}
	if err := repo.MarkEpisode(ctx, "chat:1", store.LongTermStatusDone, ""); err != nil {
		t.Fatal(err)
	}
	if pending, _, _, err := repo.EpisodeCounts(ctx); err != nil || pending != 0 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}

	validFrom := at
	edge := &store.LongTermEdge{ID: "e1", OwnerUserID: "u1", Subject: "张阿姨", Relation: "住址",
		Object: "北京", Fact: "张阿姨住在北京", ValidFrom: &validFrom, SourceRefs: []string{"chat:1"}}
	if err := repo.InsertEdge(ctx, edge); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSourceRef(ctx, "e1", "chat:2"); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSourceRef(ctx, "e1", "chat:2"); err != nil { // 幂等
		t.Fatal(err)
	}
	edges, err := repo.Edges(ctx, "u1", 0)
	if err != nil || len(edges) != 1 {
		t.Fatalf("edges=%d err=%v", len(edges), err)
	}
	if strings.Join(edges[0].SourceRefs, ",") != "chat:1,chat:2" {
		t.Fatalf("refs=%v", edges[0].SourceRefs)
	}

	// 依据来源删除：仍有其他引用时只摘除引用，不删事实。
	removed, err := repo.DeleteEdgesBySource(ctx, "u1", "chat:1")
	if err != nil || removed != 0 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	edges, _ = repo.Edges(ctx, "u1", 0)
	if len(edges) != 1 || strings.Join(edges[0].SourceRefs, ",") != "chat:2" {
		t.Fatalf("ref trim failed: %+v", edges)
	}
	removed, err = repo.DeleteEdgesBySource(ctx, "u1", "chat:2")
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if edges, _ = repo.Edges(ctx, "u1", 0); len(edges) != 0 {
		t.Fatalf("edges left=%d", len(edges))
	}

	if err := repo.DeleteEdge(ctx, "u1", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}
