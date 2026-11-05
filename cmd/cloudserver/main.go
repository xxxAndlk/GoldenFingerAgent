// Command cloudserver 提供云端共享长期记忆与跨端文件交付 API。
// 与 cmd/relay 保持独立进程；可以共用域名，但不共享业务数据职责。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"goldenfinger/agent/internal/config"
	"goldenfinger/agent/internal/deliverycloud"
	"goldenfinger/agent/internal/llm/openai"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/memorycloud"
	"goldenfinger/agent/internal/store"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "device":
			runDevice(os.Args[2:])
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "migrate":
			runMigrate(os.Args[2:])
			return
		}
	}
	runServe(nil)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "监听地址（默认取 cloud.addr / GFA_CLOUD_ADDR / :8090）")
	dbPath := fs.String("db", "", "云端数据库路径（默认取 cloud.database / GFA_CLOUD_DATABASE）")
	_ = fs.Parse(args)

	cfg := loadConfig()
	if *addr != "" {
		cfg.Cloud.Addr = *addr
	}
	if *dbPath != "" {
		cfg.Cloud.Database = *dbPath
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Connect(ctx, cfg.Cloud.Database)
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	if applied, err := db.Migrate(ctx); err != nil {
		fatal(err)
	} else if len(applied) > 0 {
		log.Printf("migrations applied: %v", applied)
	}

	var backend memorycloud.Backend
	var wake func()
	if cfg.LLM.BaseURL != "" && cfg.LLM.Model != "" {
		llmClient := openai.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
		embedder := openai.New(cfg.Embedder.BaseURL, cfg.Embedder.APIKey, cfg.Embedder.Model)
		embedder.EmbedModel = cfg.Embedder.Model
		native, nativeErr := memory.NewNative(memory.NativeConfig{
			Repos: store.NewRepos(db), LLM: llmClient, Model: cfg.LLM.Model, Embed: embedder,
			RecencyHalfLife: cfg.Memory.RecencyHalfLife,
		})
		if nativeErr != nil {
			log.Printf("cloud memory: 抽取后端初始化失败，仅接受片段不抽取: %v", nativeErr)
		} else {
			backend = native
			wake = native.Wake
			go native.Start(ctx)
			log.Printf("cloud memory: extraction enabled (model=%s)", cfg.LLM.Model)
		}
	} else {
		log.Printf("cloud memory: 未配置 llm.base_url/model，只接受片段，搜索与健康检查不可用")
	}

	memServer := memorycloud.New(memorycloud.Config{
		DB: db, Backend: backend, Wake: wake, Now: time.Now,
	})
	deliveryServer := deliverycloud.New(deliverycloud.Config{
		DB: db, Dir: cfg.Cloud.DeliveryDir, TTL: cfg.Cloud.DeliveryTTL,
		MaxBytes: cfg.Cloud.DeliveryMaxBytes, Now: time.Now, Logger: log.Printf,
	})
	go deliveryServer.StartCleaner(ctx)
	mux := http.NewServeMux()
	mux.Handle("/api/memory/v1/", memServer.Handler())
	mux.Handle("/api/delivery/v1/", deliveryServer.Handler())

	srv := &http.Server{
		Addr:              cfg.Cloud.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("cloudserver listening on %s (db: %s)", cfg.Cloud.Addr, cfg.Cloud.Database)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
	}()

	<-ctx.Done()
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func loadConfig() *config.Config {
	path := os.Getenv("GFA_CONFIG")
	if path == "" {
		path = "config.yaml"
	}
	if _, err := os.Stat(path); err == nil {
		cfg, loadErr := config.Load(path)
		if loadErr != nil {
			fatal(loadErr)
		}
		return cfg
	}
	cfg, envErr := config.LoadEnv()
	if envErr != nil {
		fatal(envErr)
	}
	return cfg
}

func runDevice(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: cloudserver device create|list|revoke [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("device create", flag.ExitOnError)
		dbPath := fs.String("db", "", "数据库路径")
		owner := fs.String("owner", "", "owner_user_id")
		name := fs.String("name", "", "设备名，如 desktop/android")
		scopes := fs.String("scopes", "memory:read,memory:write,delivery:read",
			"逗号分隔的 scope")
		_ = fs.Parse(args[1:])
		repos, db := openRepos(*dbPath)
		defer db.Close()
		token, device, err := memorycloud.CreateDevice(context.Background(), repos,
			*owner, *name, splitScopes(*scopes))
		if err != nil {
			fatal(err)
		}
		fmt.Printf("device_id: %s\ndevice_name: %s\nowner: %s\nscopes: %s\ntoken: %s\n",
			device.ID, device.DeviceName, device.OwnerUserID,
			strings.Join(device.Scopes, ","), token)
		fmt.Println("\nToken 只显示这一次，请立即保存到端侧安全存储。")
	case "list":
		fs := flag.NewFlagSet("device list", flag.ExitOnError)
		dbPath := fs.String("db", "", "数据库路径")
		owner := fs.String("owner", "", "owner_user_id")
		_ = fs.Parse(args[1:])
		repos, db := openRepos(*dbPath)
		defer db.Close()
		devices, err := repos.CloudDevices.List(context.Background(), *owner)
		if err != nil {
			fatal(err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSCOPES\tCREATED\tREVOKED\tLAST_SEEN")
		for _, d := range devices {
			lastSeen := "-"
			if d.LastSeenAt != nil {
				lastSeen = d.LastSeenAt.Format(time.RFC3339)
			}
			revoked := "-"
			if d.RevokedAt != nil {
				revoked = d.RevokedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.DeviceName,
				strings.Join(d.Scopes, ","), d.CreatedAt.Format(time.RFC3339), revoked, lastSeen)
		}
		_ = w.Flush()
	case "revoke":
		fs := flag.NewFlagSet("device revoke", flag.ExitOnError)
		dbPath := fs.String("db", "", "数据库路径")
		owner := fs.String("owner", "", "owner_user_id")
		id := fs.String("id", "", "device_id")
		_ = fs.Parse(args[1:])
		repos, db := openRepos(*dbPath)
		defer db.Close()
		if err := memorycloud.RevokeDevice(context.Background(), repos, *owner, *id, time.Now()); err != nil {
			fatal(err)
		}
		fmt.Printf("revoked: %s\n", *id)
	default:
		fmt.Fprintf(os.Stderr, "未知 device 子命令: %s\n", args[0])
		os.Exit(2)
	}
}

func runMigrate(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: cloudserver migrate export|import [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "export":
		fs := flag.NewFlagSet("migrate export", flag.ExitOnError)
		dbPath := fs.String("db", "", "旧库路径（默认 GFA_CLOUD_DATABASE / config cloud.database）")
		owner := fs.String("owner", "", "要导出的 owner_user_id")
		out := fs.String("out", "", "输出 JSON 文件")
		_ = fs.Parse(args[1:])
		if *owner == "" || *out == "" {
			fmt.Fprintln(os.Stderr, "migrate export 需要 --owner 与 --out")
			os.Exit(2)
		}
		db := openDB(*dbPath)
		defer db.Close()
		file, err := memorycloud.Export(context.Background(), db, *owner)
		if err != nil {
			fatal(err)
		}
		raw, err := memorycloud.MarshalExport(file)
		if err != nil {
			fatal(err)
		}
		tmp := *out + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			fatal(err)
		}
		if err := os.Rename(tmp, *out); err != nil {
			fatal(err)
		}
		fmt.Printf("exported: episodes=%d edges=%d -> %s\n", len(file.Episodes), len(file.Edges), *out)
	case "import":
		fs := flag.NewFlagSet("migrate import", flag.ExitOnError)
		dbPath := fs.String("db", "", "云端库路径")
		filePath := fs.String("file", "", "迁移 JSON 文件")
		dim := fs.Int("dim", 0, "云端 Embedder 维度；不一致时重建向量")
		_ = fs.Parse(args[1:])
		if *filePath == "" {
			fmt.Fprintln(os.Stderr, "migrate import 需要 --file")
			os.Exit(2)
		}
		raw, err := os.ReadFile(*filePath)
		if err != nil {
			fatal(err)
		}
		var file memorycloud.ExportFile
		if err := json.Unmarshal(raw, &file); err != nil {
			fatal(err)
		}
		db := openDB(*dbPath)
		defer db.Close()
		stats, err := memorycloud.Import(context.Background(), db, &file, *dim)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("imported: episodes=%d edges=%d vectors_rebuilt=%v\n",
			stats.Episodes, stats.Edges, stats.VectorsRebuilt)
	default:
		fmt.Fprintf(os.Stderr, "未知 migrate 子命令: %s\n", args[0])
		os.Exit(2)
	}
}

func openDB(flagDB string) *store.DB {
	path := flagDB
	if path == "" {
		path = os.Getenv("GFA_CLOUD_DATABASE")
	}
	if path == "" {
		if cfgPath := os.Getenv("GFA_CONFIG"); cfgPath != "" {
			if _, err := os.Stat(cfgPath); err == nil {
				path = loadConfig().Cloud.Database
			}
		}
	}
	if path == "" {
		path = "data/cloud.db"
	}
	db, err := store.Connect(context.Background(), path)
	if err != nil {
		fatal(err)
	}
	if _, err := db.Migrate(context.Background()); err != nil {
		db.Close()
		fatal(err)
	}
	return db
}

func openRepos(flagDB string) (*store.Repos, *store.DB) {
	db := openDB(flagDB)
	return store.NewRepos(db), db
}

func splitScopes(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "cloudserver:", err)
	os.Exit(1)
}
