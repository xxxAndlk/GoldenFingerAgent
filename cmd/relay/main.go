// Command relay 是「手机生活管家 → 电脑工作助手」的公网中继。
//
// 它的职责只有配對、鉴权和转发：
//   - 不执行任何任务，不理解业务内容；
//   - 默认不落盘（内存队列 + TTL），重启即丢未投递消息；
//   - 单二进制、无外部依赖，可在 2 核 2G 机器上与 nginx/caddy 共存。
//
// 部署示例（配合 TLS 反代）：
//
//	RELAY_ADDR=127.0.0.1:8787 ./relay
//	# caddy: relay.example.com { reverse_proxy 127.0.0.1:8787 }
//
// 生产环境必须走 HTTPS/WSS，否则配对码与消息在链路上是明文。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"goldenfinger/agent/internal/relay"
)

func main() {
	addr := flag.String("addr", envOr("RELAY_ADDR", ":8787"), "监听地址")
	maxQueue := flag.Int("max-queue", envInt("RELAY_MAX_QUEUE", 200), "每个通道每方向最多缓存的离线消息数")
	queueTTL := flag.Duration("queue-ttl", envDuration("RELAY_QUEUE_TTL", 24*time.Hour), "离线消息保留时长")
	dedupTTL := flag.Duration("dedup-ttl", envDuration("RELAY_DEDUP_TTL", 10*time.Minute), "消息去重窗口")
	codeTTL := flag.Duration("code-ttl", envDuration("RELAY_CODE_TTL", 5*time.Minute), "配对码有效期")
	maxEndpoints := flag.Int("max-endpoints", envInt("RELAY_MAX_ENDPOINTS", 2000), "最大在线/登记电脑数")
	maxPayload := flag.Int("max-payload", envInt("RELAY_MAX_PAYLOAD", 256<<10), "单条报文上限（字节）")
	// 没有域名时用自签证书直接对外提供 TLS：证书要与手机/电脑信任的 CA 一致。
	tlsCert := flag.String("tls-cert", os.Getenv("RELAY_TLS_CERT"), "TLS 证书路径（留空则明文监听，仅供本机反代）")
	tlsKey := flag.String("tls-key", os.Getenv("RELAY_TLS_KEY"), "TLS 私钥路径")
	flag.Parse()

	cfg := relay.Config{
		MaxQueuePerSide: *maxQueue,
		QueueTTL:        *queueTTL,
		DedupTTL:        *dedupTTL,
		CodeTTL:         *codeTTL,
		MaxEndpoints:    *maxEndpoints,
		MaxPayloadBytes: *maxPayload,
	}
	hub := relay.NewHub(cfg)
	server := relay.NewServer(hub, cfg)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// 中继不跑 CPU 密集任务，超时设短一些，避免慢连接占用小机器。
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   0, // WebSocket 长连接由 writePump 自己控制超时
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if *tlsCert != "" && *tlsKey != "" {
			log.Printf("relay listening on %s (TLS, queue=%d ttl=%s)", *addr, *maxQueue, *queueTTL)
			err := srv.ListenAndServeTLS(*tlsCert, *tlsKey)
			if err != nil && err != http.ErrServerClosed {
				fmt.Fprintln(os.Stderr, "relay:", err)
				os.Exit(1)
			}
			return
		}
		log.Printf("relay listening on %s (queue=%d ttl=%s)", *addr, *maxQueue, *queueTTL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "relay:", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Printf("relay stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
