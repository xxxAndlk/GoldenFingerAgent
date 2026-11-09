package relay

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
)

// NewTLSClient 构造信任指定 CA 的 HTTP 客户端。
//
// 中继用 IP 直连时只能自签，而自签证书不在系统信任库里：
// 让客户端额外信任这份 CA 证书，比 InsecureSkipVerify 安全得多 ——
// 握手仍然校验主机名与签名，只是把签发者限定为我们自己的一纸证书。
// caFile 为空时返回默认客户端（走系统信任库）。
func NewTLSClient(caFile string) (*http.Client, error) {
	tlsConfig, err := tlsConfigFor(caFile)
	if err != nil {
		return nil, err
	}
	if tlsConfig == nil {
		return &http.Client{}, nil
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}, nil
}

// NewTLSDialer 构造信任指定 CA 的 WebSocket 拨号器。
func NewTLSDialer(caFile string) (*websocket.Dialer, error) {
	tlsConfig, err := tlsConfigFor(caFile)
	if err != nil {
		return nil, err
	}
	return &websocket.Dialer{TLSClientConfig: tlsConfig}, nil
}

func tlsConfigFor(caFile string) (*tls.Config, error) {
	if caFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		// 文件不存在代表「没配自签 CA」，不是错误：照常走系统信任库。
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 CA 证书失败: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA 证书解析失败：%s", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
