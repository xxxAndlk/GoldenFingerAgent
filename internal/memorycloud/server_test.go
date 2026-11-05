package memorycloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
)

type fakeBackend struct {
	mu        sync.Mutex
	facts     []memory.MemoryFact
	owners    []string
	forgotten []memory.ForgetRequest
}

func (f *fakeBackend) Search(_ context.Context, query memory.MemoryQuery) ([]memory.MemoryFact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners = append(f.owners, query.OwnerID)
	return append([]memory.MemoryFact(nil), f.facts...), nil
}

func (f *fakeBackend) Forget(_ context.Context, request memory.ForgetRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, request)
	return nil
}

func (f *fakeBackend) Health(context.Context) (memory.MemoryHealth, error) {
	return memory.MemoryHealth{Ready: true, Backend: "fake", Detail: "ok"}, nil
}

type fixture struct {
	server *Server
	ts     *httptest.Server
	repos  *store.Repos
	db     *store.DB
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, filepath.Join(t.TempDir(), "cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	now := time.Date(2026, 11, 4, 18, 40, 0, 0, time.UTC)
	srv := New(Config{DB: db, Backend: &fakeBackend{}, Now: func() time.Time { return now }})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &fixture{server: srv, ts: ts, repos: store.NewRepos(db), db: db, now: now}
}

func (f *fixture) createDevice(t *testing.T, owner, name string, scopes []string) (string, *store.CloudDevice) {
	t.Helper()
	token, device, err := CreateDevice(context.Background(), f.repos, owner, name, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return token, device
}

func doJSON(t *testing.T, method, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp, decoded
}

func (f *fixture) episode(t *testing.T, token, source, text, role string) (*http.Response, map[string]any) {
	t.Helper()
	return doJSON(t, http.MethodPost, f.ts.URL+"/api/memory/v1/episodes", token, map[string]any{
		"source_id": source, "text": text, "source": "chat", "role": role,
		"reference_time": f.now.Format(time.RFC3339),
	})
}

func TestEpisodeIdempotencyAndConflict(t *testing.T) {
	f := newFixture(t)
	token, _ := f.createDevice(t, "u1", "desktop", []string{ScopeMemoryRead, ScopeMemoryWrite})

	resp, body := f.episode(t, token, "chat:1", "以后总结尽量简短。", "owner")
	if resp.StatusCode != 200 || body["status"] != "accepted" {
		t.Fatalf("first upload: code=%d body=%v", resp.StatusCode, body)
	}
	resp, body = f.episode(t, token, "chat:1", "以后总结尽量简短。", "owner")
	if resp.StatusCode != 200 || body["status"] != "duplicate" {
		t.Fatalf("duplicate upload: code=%d body=%v", resp.StatusCode, body)
	}
	pending, done, failed, err := f.repos.LongTerm.EpisodeCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending+done+failed != 1 {
		t.Fatalf("episode count = %d, want 1", pending+done+failed)
	}
	resp, _ = f.episode(t, token, "chat:1", "完全不同的一句話。", "owner")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict: code=%d, want 409", resp.StatusCode)
	}
}

func TestEpisodeForgottenTombstone(t *testing.T) {
	f := newFixture(t)
	token, _ := f.createDevice(t, "u1", "desktop", []string{ScopeMemoryRead, ScopeMemoryWrite})
	if resp, _ := f.episode(t, token, "chat:9", "记住这条。", "owner"); resp.StatusCode != 200 {
		t.Fatalf("upload failed: %d", resp.StatusCode)
	}
	resp, body := doJSON(t, http.MethodPost, f.ts.URL+"/api/memory/v1/forget", token,
		map[string]any{"source_id": "chat:9"})
	if resp.StatusCode != 200 || body["status"] != "forgotten" {
		t.Fatalf("forget: code=%d body=%v", resp.StatusCode, body)
	}
	if pending, done, failed, _ := f.repos.LongTerm.EpisodeCounts(context.Background()); pending+done+failed != 0 {
		t.Fatalf("episode still present after forget")
	}
	// 其他设备未发送的同一事件不能复活记忆。
	resp, _ = f.episode(t, token, "chat:9", "记住这条。", "owner")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("re-upload after forget: code=%d, want 410", resp.StatusCode)
	}
}

func TestSearchMapsSourceRefsBackToOrigin(t *testing.T) {
	f := newFixture(t)
	token, _ := f.createDevice(t, "u1", "desktop", []string{ScopeMemoryRead, ScopeMemoryWrite})
	if resp, _ := f.episode(t, token, "chat:1", "以后总结先列结论。", "owner"); resp.StatusCode != 200 {
		t.Fatalf("upload failed: %d", resp.StatusCode)
	}
	backend := f.server.backend.(*fakeBackend)
	backend.facts = []memory.MemoryFact{{
		ID: "fact-1", Text: "用户喜欢先列结论。", Status: "inferred",
		SourceRefs: []string{StoredSourceID("u1", "chat:1")},
	}}
	resp, body := doJSON(t, http.MethodPost, f.ts.URL+"/api/memory/v1/search", token,
		map[string]any{"query": "总结格式", "limit": 5})
	if resp.StatusCode != 200 {
		t.Fatalf("search code=%d body=%v", resp.StatusCode, body)
	}
	facts, _ := body["facts"].([]any)
	if len(facts) != 1 {
		t.Fatalf("facts = %v", body["facts"])
	}
	item := facts[0].(map[string]any)
	refs, _ := item["source_refs"].([]any)
	if len(refs) != 1 || refs[0] != "chat:1" {
		t.Fatalf("source refs = %v, want [chat:1]", refs)
	}
	if len(backend.owners) != 1 || backend.owners[0] != "u1" {
		t.Fatalf("owner = %v, want u1", backend.owners)
	}
}

func TestOwnerIsolatedByDeviceToken(t *testing.T) {
	f := newFixture(t)
	_, _ = f.createDevice(t, "u1", "desktop", []string{ScopeMemoryRead, ScopeMemoryWrite})
	tokenB, _ := f.createDevice(t, "u2", "android", []string{ScopeMemoryRead, ScopeMemoryWrite})
	backend := f.server.backend.(*fakeBackend)

	_, _ = doJSON(t, http.MethodPost, f.ts.URL+"/api/memory/v1/search", tokenB,
		map[string]any{"query": "任意问题"})
	if len(backend.owners) != 1 || backend.owners[0] != "u2" {
		t.Fatalf("search owner = %v, want u2", backend.owners)
	}
}

func TestScopeAndAuth(t *testing.T) {
	f := newFixture(t)
	readOnly, _ := f.createDevice(t, "u1", "phone", []string{ScopeMemoryRead})

	resp, _ := f.episode(t, readOnly, "chat:2", "写入", "owner")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only write: code=%d, want 403", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodPost, f.ts.URL+"/api/memory/v1/search", "bad-token",
		map[string]any{"query": "x"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: code=%d, want 401", resp.StatusCode)
	}
}

func TestRevokedDeviceRejected(t *testing.T) {
	f := newFixture(t)
	token, device := f.createDevice(t, "u1", "desktop", []string{ScopeMemoryRead})
	if err := RevokeDevice(context.Background(), f.repos, "u1", device.ID, f.now); err != nil {
		t.Fatal(err)
	}
	resp, _ := doJSON(t, http.MethodPost, f.ts.URL+"/api/memory/v1/search", token,
		map[string]any{"query": "x"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: code=%d, want 401", resp.StatusCode)
	}
}

func TestValidation(t *testing.T) {
	f := newFixture(t)
	token, _ := f.createDevice(t, "u1", "desktop", []string{ScopeMemoryRead, ScopeMemoryWrite})
	for _, body := range []map[string]any{
		{"source_id": "", "text": "x", "role": "owner"},
		{"source_id": "chat:1", "text": "", "role": "owner"},
		{"source_id": "chat:1", "text": "x", "role": "stranger"},
		{"source_id": "chat:1", "text": "x", "role": "owner", "content_hash": "sha256:deadbeef"},
	} {
		resp, _ := doJSON(t, http.MethodPost, f.ts.URL+"/api/memory/v1/episodes", token, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %v: code=%d, want 400", body, resp.StatusCode)
		}
	}
}

func TestHealthCountsOnlyCallingOwner(t *testing.T) {
	f := newFixture(t)
	tokenA, _ := f.createDevice(t, "owner-a", "desktop", []string{ScopeMemoryRead, ScopeMemoryWrite})
	tokenB, _ := f.createDevice(t, "owner-b", "desktop", []string{ScopeMemoryRead, ScopeMemoryWrite})
	if resp, _ := f.episode(t, tokenA, "chat:a1", "A 的记忆", "owner"); resp.StatusCode != 200 {
		t.Fatalf("A upload failed: %d", resp.StatusCode)
	}
	if resp, _ := f.episode(t, tokenB, "chat:b1", "B 的记忆", "owner"); resp.StatusCode != 200 {
		t.Fatalf("B upload failed: %d", resp.StatusCode)
	}
	resp, body := doJSON(t, http.MethodGet, f.ts.URL+"/api/memory/v1/health", tokenA, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("health code=%d body=%v", resp.StatusCode, body)
	}
	if fmt.Sprint(body["pending"]) != "1" {
		t.Fatalf("owner A pending=%v, want 1 (must not include owner B)", body["pending"])
	}
}

func TestHealth(t *testing.T) {
	f := newFixture(t)
	token, _ := f.createDevice(t, "u1", "desktop", []string{ScopeMemoryRead})
	resp, body := doJSON(t, http.MethodGet, f.ts.URL+"/api/memory/v1/health", token, nil)
	if resp.StatusCode != 200 || body["ready"] != true {
		t.Fatalf("health: code=%d body=%v", resp.StatusCode, body)
	}
	if fmt.Sprint(body["backend"]) != "cloud:fake" {
		t.Fatalf("backend = %v", body["backend"])
	}
}
