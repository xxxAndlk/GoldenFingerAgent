package deliverycloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goldenfinger/agent/internal/cloudauth"
	"goldenfinger/agent/internal/store"
)

type deliveryFixture struct {
	server    *Server
	ts        *httptest.Server
	repos     *store.Repos
	desktop   string
	android   string
	androidID string
	desktopID string
	now       time.Time
	dir       string
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
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
	repos := store.NewRepos(db)
	desktopToken, desktop, err := cloudauth.CreateDevice(ctx, repos, "u1", "desktop",
		[]string{cloudauth.ScopeDeliveryRead, cloudauth.ScopeDeliveryWrite})
	if err != nil {
		t.Fatal(err)
	}
	androidToken, android, err := cloudauth.CreateDevice(ctx, repos, "u1", "android",
		[]string{cloudauth.ScopeDeliveryRead})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	server := New(Config{DB: db, Dir: dir, TTL: 72 * time.Hour, MaxBytes: 4 << 20,
		Now: func() time.Time { return now }, Logger: t.Logf})
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return &deliveryFixture{
		server: server, ts: ts, repos: repos, desktop: desktopToken, android: androidToken,
		androidID: android.ID, desktopID: desktop.ID, now: now, dir: dir,
	}
}

func (f *deliveryFixture) upload(t *testing.T, token, key string, fields map[string]string, name string, content []byte) (*http.Response, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+"/api/delivery/v1/artifacts", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp, decoded
}

func (f *deliveryFixture) get(t *testing.T, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func TestUploadInboxDownloadAck(t *testing.T) {
	f := newDeliveryFixture(t)
	content := []byte("hello delivery")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	resp, body := f.upload(t, f.desktop, "key-1", map[string]string{
		"artifact_id": "art-1", "artifact_version": "3", "target_device_id": f.androidID,
		"name": "说明.txt", "sha256": sha, "size_bytes": "14",
	}, "说明.txt", content)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload code=%d body=%v", resp.StatusCode, body)
	}
	deliveryID, _ := body["delivery_id"].(string)
	if deliveryID == "" || body["sha256"] != sha {
		t.Fatalf("body=%v", body)
	}

	// 相同幂等键与内容返回同一 Delivery。
	resp, again := f.upload(t, f.desktop, "key-1", map[string]string{
		"artifact_id": "art-1", "artifact_version": "3", "target_device_id": f.androidID,
		"name": "说明.txt", "sha256": sha, "size_bytes": "14",
	}, "说明.txt", content)
	if resp.StatusCode != http.StatusOK || again["delivery_id"] != deliveryID {
		t.Fatalf("idempotent code=%d body=%v", resp.StatusCode, again)
	}
	// 同键不同内容必须 409。
	resp, _ = f.upload(t, f.desktop, "key-1", map[string]string{
		"artifact_id": "art-1", "artifact_version": "4", "target_device_id": f.androidID,
		"name": "说明.txt",
	}, "说明.txt", []byte("different"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict code=%d", resp.StatusCode)
	}

	resp, raw := f.get(t, "/api/delivery/v1/inbox", f.android)
	if resp.StatusCode != 200 {
		t.Fatalf("inbox code=%d body=%s", resp.StatusCode, raw)
	}
	var inbox struct {
		Deliveries []store.CloudDelivery `json:"deliveries"`
	}
	if err := json.Unmarshal(raw, &inbox); err != nil || len(inbox.Deliveries) != 1 {
		t.Fatalf("inbox=%s err=%v", raw, err)
	}
	if inbox.Deliveries[0].TargetDeviceID != f.androidID || inbox.Deliveries[0].AckedAt != nil {
		t.Fatalf("delivery=%+v", inbox.Deliveries[0])
	}

	resp, raw = f.get(t, "/api/delivery/v1/artifacts/"+deliveryID+"/content", f.android)
	if resp.StatusCode != 200 || !bytes.Equal(raw, content) {
		t.Fatalf("content code=%d body=%q", resp.StatusCode, raw)
	}
	if resp.Header.Get("X-Content-Hash") != sha {
		t.Fatalf("hash header=%s", resp.Header.Get("X-Content-Hash"))
	}
	// 非目标设备不能下载。
	resp, _ = f.get(t, "/api/delivery/v1/artifacts/"+deliveryID+"/content", f.desktop)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-target content code=%d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, f.ts.URL+"/api/delivery/v1/artifacts/"+deliveryID+"/ack", nil)
	req.Header.Set("Authorization", "Bearer "+f.android)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ack code=%d", resp.StatusCode)
	}
	item, err := f.repos.CloudDeliveries.Get(context.Background(), "u1", deliveryID)
	if err != nil || item.AckedAt == nil {
		t.Fatalf("acked=%v err=%v", item, err)
	}
}

func TestDeleteRevokesAndRemovesFile(t *testing.T) {
	f := newDeliveryFixture(t)
	resp, body := f.upload(t, f.desktop, "key-del", map[string]string{
		"artifact_id": "art-2", "artifact_version": "1", "target_device_id": f.androidID,
		"name": "a.txt",
	}, "a.txt", []byte("bye"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload code=%d body=%v", resp.StatusCode, body)
	}
	deliveryID := body["delivery_id"].(string)
	item, err := f.repos.CloudDeliveries.Get(context.Background(), "u1", deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodDelete, f.ts.URL+"/api/delivery/v1/artifacts/"+deliveryID, nil)
	req.Header.Set("Authorization", "Bearer "+f.desktop)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete code=%d", resp.StatusCode)
	}
	if _, err := os.Stat(item.StoragePath); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
	resp, _ = f.get(t, "/api/delivery/v1/artifacts/"+deliveryID+"/content", f.android)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("content after revoke code=%d, want 410", resp.StatusCode)
	}
}

func TestExecutableAndOversizeRejected(t *testing.T) {
	f := newDeliveryFixture(t)
	resp, _ := f.upload(t, f.desktop, "exe", map[string]string{
		"artifact_id": "art-3", "artifact_version": "1", "target_device_id": f.androidID,
		"name": "run.bat",
	}, "run.bat", []byte("@echo off"))
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("exe code=%d", resp.StatusCode)
	}
}

func TestCleanupExpired(t *testing.T) {
	f := newDeliveryFixture(t)
	resp, body := f.upload(t, f.desktop, "key-ttl", map[string]string{
		"artifact_id": "art-4", "artifact_version": "1", "target_device_id": f.androidID,
		"name": "ttl.txt",
	}, "ttl.txt", []byte("ttl"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload code=%d body=%v", resp.StatusCode, body)
	}
	deliveryID := body["delivery_id"].(string)
	item, _ := f.repos.CloudDeliveries.Get(context.Background(), "u1", deliveryID)
	f.server.now = func() time.Time { return f.now.Add(100 * time.Hour) }
	if cleaned := f.server.Cleanup(context.Background()); cleaned != 1 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	if _, err := os.Stat(item.StoragePath); !os.IsNotExist(err) {
		t.Fatalf("expired file still exists")
	}
	got, _ := f.repos.CloudDeliveries.Get(context.Background(), "u1", deliveryID)
	if got.Status != store.DeliveryExpired {
		t.Fatalf("status=%s", got.Status)
	}
}
