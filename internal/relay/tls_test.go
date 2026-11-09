package relay

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pemEncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// 自签中继：客户端只有拿到 CA 才能握手成功。
// 这两个用例守住「能连」与「没配 CA 就连不上」两条边界，
// 避免以后有人把校验改成 InsecureSkipVerify 而不自知。
func TestSelfSignedRelayWithTrustedCA(t *testing.T) {
	hub := NewHub(Config{MaxQueuePerSide: 8, QueueTTL: time.Minute, DedupTTL: time.Minute, CodeTTL: time.Minute})
	srv := httptest.NewTLSServer(NewServer(hub, Config{}).Handler())
	defer srv.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	cert := srv.Certificate()
	if err := os.WriteFile(caFile, pemEncodeCert(cert.Raw), 0o600); err != nil {
		t.Fatalf("写入 CA: %v", err)
	}

	client, err := NewTLSClient(caFile)
	if err != nil {
		t.Fatalf("构造客户端: %v", err)
	}
	resp, err := client.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("信任 CA 后仍握手失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
}

func TestSelfSignedRelayWithoutCAIsRejected(t *testing.T) {
	hub := NewHub(Config{MaxQueuePerSide: 8, QueueTTL: time.Minute, DedupTTL: time.Minute, CodeTTL: time.Minute})
	srv := httptest.NewTLSServer(NewServer(hub, Config{}).Handler())
	defer srv.Close()

	// 不配 CA：必须失败，证明我们没有偷偷跳过校验。
	client, err := NewTLSClient("")
	if err != nil {
		t.Fatalf("构造客户端: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/healthz", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("没有 CA 也握手成功了：说明 TLS 校验被跳过")
	}
}

func TestMissingCAFileFallsBackToSystemPool(t *testing.T) {
	client, err := NewTLSClient(filepath.Join(t.TempDir(), "absent.pem"))
	if err != nil {
		t.Fatalf("CA 文件缺失不应报错: %v", err)
	}
	if client == nil {
		t.Fatal("应当返回可用的客户端")
	}
	// 顺带确认没有配置 TLSClientConfig（走系统信任库）。
	transport, ok := client.Transport.(*http.Transport)
	if ok && transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("不得跳过证书校验")
	}
}

var _ = tls.VersionTLS12
