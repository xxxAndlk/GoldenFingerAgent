package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

type cloudStub struct {
	mu       sync.Mutex
	uploads  []map[string]any
	searches []map[string]any
	forgets  []map[string]any
	facts    []map[string]any
	status   int
}

func (c *cloudStub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/memory/v1/episodes", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.uploads = append(c.uploads, body)
		status := c.status
		c.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "forced"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	})
	mux.HandleFunc("POST /api/memory/v1/search", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.searches = append(c.searches, body)
		facts := c.facts
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"facts": facts})
	})
	mux.HandleFunc("POST /api/memory/v1/forget", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.forgets = append(c.forgets, body)
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "forgotten"})
	})
	mux.HandleFunc("GET /api/memory/v1/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "backend": "stub"})
	})
	return mux
}

func (c *cloudStub) setStatus(status int) {
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
}

func (c *cloudStub) uploadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.uploads)
}

func (c *cloudStub) setFacts(facts []map[string]any) {
	c.mu.Lock()
	c.facts = facts
	c.mu.Unlock()
}

type remoteFixture struct {
	remote  *Remote
	localDB *store.DB
	cloud   *cloudStub
	now     time.Time
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	ctx := context.Background()
	localDB, err := store.Connect(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(localDB.Close)

	cloud := &cloudStub{}
	ts := httptest.NewServer(cloud.Handler())
	t.Cleanup(ts.Close)
	now := time.Date(2026, 11, 4, 18, 40, 0, 0, time.UTC)
	remote, err := NewRemote(RemoteConfig{
		BaseURL: ts.URL, Token: "token", DeviceID: "desktop", OwnerID: "u1", DB: localDB,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &remoteFixture{remote: remote, localDB: localDB, cloud: cloud, now: now}
}

func TestRemoteAppendFlushAndSearch(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	if err := f.remote.Append(ctx, Episode{
		SourceID: "chat:1", OwnerID: "u1", Text: "以后总结先列结论。", Source: "chat",
		Role: RoleOwner, ReferenceTime: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	pending, _, _, err := store.NewRepos(f.localDB).MemoryOutbox.Counts(ctx, "u1")
	if err != nil || pending != 1 {
		t.Fatalf("outbox pending=%d err=%v", pending, err)
	}
	if sent := f.remote.Flush(ctx); sent != 1 {
		t.Fatalf("flush sent=%d, want 1", sent)
	}
	pending, _, _, _ = store.NewRepos(f.localDB).MemoryOutbox.Counts(ctx, "u1")
	if pending != 0 {
		t.Fatalf("pending after flush = %d", pending)
	}
	if f.cloud.uploadCount() != 1 {
		t.Fatalf("uploads = %d", f.cloud.uploadCount())
	}
	if f.cloud.uploads[0]["source_id"] != "chat:1" || f.cloud.uploads[0]["role"] != "owner" {
		t.Fatalf("payload = %v", f.cloud.uploads[0])
	}

	f.cloud.setFacts([]map[string]any{{
		"id": "f1", "text": "用户希望总结先列结论。", "status": "inferred",
		"source_refs": []string{"chat:1"},
	}})
	facts, err := f.remote.Search(ctx, MemoryQuery{OwnerID: "u1", Query: "总结格式"})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Text != "用户希望总结先列结论。" || facts[0].SourceRefs[0] != "chat:1" {
		t.Fatalf("facts=%+v", facts)
	}
	if f.cloud.searches[0]["query"] != "总结格式" {
		t.Fatalf("search payload = %v", f.cloud.searches[0])
	}
}

func TestRemoteRetryOnServerError(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	f.cloud.setStatus(http.StatusInternalServerError)
	if err := f.remote.Append(ctx, Episode{SourceID: "chat:2", OwnerID: "u1", Text: "x", Role: RoleOwner}); err != nil {
		t.Fatal(err)
	}
	if sent := f.remote.Flush(ctx); sent != 0 {
		t.Fatalf("sent=%d", sent)
	}
	entries, err := store.NewRepos(f.localDB).MemoryOutbox.Due(ctx, f.now.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Attempts != 1 || entries[0].NextRetryAt == nil {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestRemotePauseOnUnauthorized(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	f.cloud.setStatus(http.StatusUnauthorized)
	if err := f.remote.Append(ctx, Episode{SourceID: "chat:3", OwnerID: "u1", Text: "x", Role: RoleOwner}); err != nil {
		t.Fatal(err)
	}
	f.remote.Flush(ctx)
	if !f.remote.Paused() {
		t.Fatal("remote should be paused after 401")
	}
	_, _, paused, _ := store.NewRepos(f.localDB).MemoryOutbox.Counts(ctx, "u1")
	if paused != 1 {
		t.Fatalf("paused=%d, want 1", paused)
	}
	if resumed, err := f.remote.Resume(ctx); err != nil || resumed != 1 {
		t.Fatalf("resume=%d err=%v", resumed, err)
	}
}

func TestRemoteForgetQueuesDeleteAndSendsOnFlush(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	if err := f.remote.Append(ctx, Episode{SourceID: "chat:4", OwnerID: "u1", Text: "x", Role: RoleOwner}); err != nil {
		t.Fatal(err)
	}
	if err := f.remote.Forget(ctx, ForgetRequest{OwnerID: "u1", SourceID: "chat:4"}); err != nil {
		t.Fatal(err)
	}
	// 遗忘先落本地队列：断网时删除不能丢。
	pending, _, _, _ := store.NewRepos(f.localDB).MemoryOutbox.Counts(ctx, "u1")
	if pending != 1 {
		t.Fatalf("pending after forget = %d", pending)
	}
	if len(f.cloud.forgets) != 0 {
		t.Fatalf("遗忘不应在排队前就发出去：%v", f.cloud.forgets)
	}
	// 补发之后云端才真的删。
	if sent := f.remote.Flush(ctx); sent != 1 {
		t.Fatalf("sent = %d", sent)
	}
	if len(f.cloud.forgets) != 1 || f.cloud.forgets[0]["source_id"] != "chat:4" {
		t.Fatalf("forgets = %v", f.cloud.forgets)
	}
	pending, _, _, _ = store.NewRepos(f.localDB).MemoryOutbox.Counts(ctx, "u1")
	if pending != 0 {
		t.Fatalf("pending after flush = %d", pending)
	}
}

func TestBuildContextForQueryIncludesLongTermFacts(t *testing.T) {
	ctx := context.Background()
	db, err := store.Connect(ctx, filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	repos := store.NewRepos(db)
	svc := NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit, nil,
		nlu.Thresholds{}, 30*24*time.Hour, nil)
	svc.SetLongTermMemory(&staticLongTerm{facts: []MemoryFact{{
		ID: "f1", Text: "用户希望总结先列结论，再列三个要点。", Status: "confirmed",
	}}})
	block, err := svc.BuildContextForQuery(ctx, "u1", "general", "Asia/Shanghai", "总结格式", 1500)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "相关长期记忆") || !strings.Contains(block, "先列结论") {
		t.Fatalf("block missing long-term fact:\n%s", block)
	}
}

type staticLongTerm struct {
	facts []MemoryFact
}

func (s *staticLongTerm) Append(context.Context, Episode) error { return nil }
func (s *staticLongTerm) Search(context.Context, MemoryQuery) ([]MemoryFact, error) {
	return append([]MemoryFact(nil), s.facts...), nil
}
func (s *staticLongTerm) Forget(context.Context, ForgetRequest) error { return nil }
func (s *staticLongTerm) Health(context.Context) (MemoryHealth, error) {
	return MemoryHealth{Ready: true}, nil
}
