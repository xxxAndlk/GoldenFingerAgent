// Package gosvc 是金手指管家 Go 服务的 gomobile bind 封装：
// 在 Android 上以「127.0.0.1 随机端口 + 随机 token」方式提供完整 HTTP 服务，
// 供 App 的 WebView 与无障碍通道（WS/HTTP）消费。装配逻辑与 cmd/server/main.go
// 同构，但全部数据（config/settings/SQLite/device.id/token）落在 dataDir（App 私有目录）。
package gosvc

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/config"
	"goldenfinger/agent/internal/deliveryclient"
	"goldenfinger/agent/internal/device"
	"goldenfinger/agent/internal/extsvc"
	"goldenfinger/agent/internal/extsvc/deepseek"
	"goldenfinger/agent/internal/extsvc/stub"
	"goldenfinger/agent/internal/httpapi"
	"goldenfinger/agent/internal/inbox"
	"goldenfinger/agent/internal/intent"
	"goldenfinger/agent/internal/llm/openai"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/relay"
	"goldenfinger/agent/internal/scheduler"
	"goldenfinger/agent/internal/secretstore"
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
	"goldenfinger/agent/internal/voice"
)

// SecretStore 由 Android Kotlin 用 Keystore 实现 AES-GCM 加解密。
// gomobile 会把它导出为 Java/Kotlin 接口，密钥材料不会进入 Go 或文件系统。
type SecretStore interface {
	Encrypt(plain []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// SetSecretStore 必须在 StartServer 前调用，确保旧 Token 迁移和新 Token 保存都加密。
func SetSecretStore(store SecretStore) {
	secretstore.SetBridge(store)
}

// webAssets 内嵌手机端 Web 首页与其前端依赖。
// 内容由 scripts/sync-webassets.sh 从 web/ 同步，避免手机端再维护一份分叉页面。
//
//go:embed webassets
var webAssets embed.FS

type serverState struct {
	srv    *http.Server
	db     *store.DB
	hub    *device.Hub
	phone  *relay.Phone
	cancel context.CancelFunc
}

var (
	stateMu sync.Mutex
	running *serverState
)

// StartServer 启动服务并返回 info JSON：
// {"addr":"127.0.0.1:PORT","token":"...","device_id":"..."}。
// 仅监听 127.0.0.1 的随机端口；所有请求（含 /ws/device 握手与静态页面）
// 都须携带匹配的 X-Token 请求头或 query token。
func StartServer(dataDir string) (info string, err error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if running != nil {
		return "", fmt.Errorf("gosvc: 服务已在运行")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("gosvc: 创建数据目录: %w", err)
	}
	cfg, err := ensureConfig(dataDir)
	if err != nil {
		return "", err
	}
	token, err := ensureSecret(dataDir, "token", 32)
	if err != nil {
		return "", err
	}
	devID, err := ensureSecret(dataDir, "device.id", 8)
	if err != nil {
		return "", err
	}

	ctx := context.Background()
	db, err := store.Connect(ctx, filepath.Join(dataDir, "gfa.db"))
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			db.Close()
		}
	}()
	if _, err := db.Migrate(ctx); err != nil {
		return "", fmt.Errorf("gosvc: 迁移数据库: %w", err)
	}
	repos := store.NewRepos(db)

	// ---- 外部服务：LLM/向量为真实云服务，其余用开发桩（与 main.go 一致） ----
	llmClient := openai.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	embedder := openai.New(cfg.Embedder.BaseURL, cfg.Embedder.APIKey, cfg.Embedder.Model)
	embedder.EmbedModel = cfg.Embedder.Model

	var weather extsvc.WeatherService = stub.Weather{}
	// 手机端与电脑端一致：联网搜索/抓取复用当前模型的 DeepSeek Key，
	// 不再退回 Firecrawl 或开发桩数据。
	webClient := deepseek.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.Search.Count)

	outbox := &httpapi.Outbox{}
	dispatcher := scheduler.DeliverFunc(func(ctx context.Context, u *store.User, r *store.Reminder, body string) error {
		switch r.Channel {
		case "push":
			return stub.Push{}.Push(ctx, "device-token", extsvc.PushMessage{Title: "管家提醒", Body: body, Level: r.Level})
		case "sms":
			return stub.SMS{}.Send(ctx, "phone", body)
		default:
			outbox.PushRoute(u.ID, body, httpapi.OutboxRouteTasks, time.Now())
		}
		return nil
	})

	th := nlu.Thresholds{
		TaskAuto:      cfg.Thresholds.TaskAuto,
		TaskClarify:   cfg.Thresholds.TaskClarify,
		PersonClarify: cfg.Thresholds.PersonClarify,
		FactConfirmed: cfg.Thresholds.FactConfirmed,
	}
	mem := memory.NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit,
		embedder, th, cfg.Memory.RecencyHalfLife, nil)
	// 共享记忆槽位：手机改备注后，电脑端能从同一份长期记忆里检索到。
	mem.SetSharedSlots(repos.SharedSlots)

	st, err := settings.Open(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		return "", fmt.Errorf("gosvc: 打开设置: %w", err)
	}
	// 云模式只使用 Remote；本机模式才启动 Native 抽取，避免双主写。
	var nativeMemory *memory.Native
	var remoteMemory *memory.Remote
	cloud := st.CloudMemory()
	cloudBase := cloud.BaseURL
	if env := os.Getenv("GFA_CLOUD_URL"); env != "" {
		cloudBase = env
	}
	cloudToken := st.CloudToken()
	if env := os.Getenv("GFA_CLOUD_TOKEN"); env != "" {
		cloudToken = env
	}
	cloudActive := cloud.Enabled || os.Getenv("GFA_CLOUD_URL") != ""
	if cloudActive {
		if cloudBase == "" || cloudToken == "" {
			log.Printf("[gosvc] 云端记忆缺少 base_url/token，保持本机模式")
		} else {
			ownerID := ""
			if users, uerr := repos.Users.ListAll(ctx); uerr == nil && len(users) > 0 {
				ownerID = users[0].ID
			}
			remote, rerr := memory.NewRemote(memory.RemoteConfig{
				BaseURL: cloudBase, Token: cloudToken, DeviceID: cloud.DeviceID,
				OwnerID: ownerID, DB: db,
			})
			if rerr != nil {
				log.Printf("[gosvc] 云端记忆初始化失败，保持本机模式: %v", rerr)
			} else {
				remoteMemory = remote
				mem.SetLongTermMemory(remote)
				log.Printf("[gosvc] 云端共享记忆 enabled (base=%s device=%s)", cloudBase, cloud.DeviceID)
			}
		}
	}
	var deliveryClient *deliveryclient.Client
	if cloudActive && cloud.DeliveryEnabled && cloudBase != "" && cloudToken != "" {
		client, derr := deliveryclient.New(deliveryclient.Config{
			BaseURL: cloudBase, Token: cloudToken, DeviceID: cloud.DeviceID,
		})
		if derr != nil {
			log.Printf("[gosvc] 云端交付客户端初始化失败: %v", derr)
		} else {
			deliveryClient = client
		}
	}
	if remoteMemory == nil {
		nativeMemory, err = memory.NewNative(memory.NativeConfig{
			Repos: repos, LLM: llmClient, Model: cfg.LLM.Model, Embed: embedder,
			RecencyHalfLife: cfg.Memory.RecencyHalfLife,
		})
		if err != nil {
			log.Printf("[gosvc] 长期记忆初始化失败，降级为 SQLite 检索: %v", err)
		} else {
			mem.SetLongTermMemory(nativeMemory)
		}
	}

	// 通知中心：与电脑端同构装配，否则 /api/inbox* 在手机上会全部 503，
	// 通知监听（NotifyListenerService）上报的事件就无处可去。
	inboxSvc := inbox.NewService(repos, embedder, time.Now, cfg.Embedder.Dim)
	inboxSvc.Notify = func(_ context.Context, ownerID, body string) {
		outbox.PushRoute(ownerID, body, httpapi.OutboxRouteInbox, time.Now())
	}

	policy := scheduler.Policy{
		DNDStartHour:       22,
		DNDEndHour:         7,
		ChildNightSilence:  cfg.DND.ChildNightSilence,
		UrgentBreaksDNDFor: map[string]bool{},
		AckTimeout:         cfg.Scheduler.AckTimeout,
		MaxLevel:           cfg.Scheduler.MaxLevel,
		MaxDailyPush:       cfg.Scheduler.MaxDailyPush,
	}
	for _, ut := range cfg.DND.UrgentBreaksDNDFor {
		policy.UrgentBreaksDNDFor[ut] = true
	}

	guard := compliance.NewGuard(repos.Consents, repos.Audit, repos.Users)

	var sched *scheduler.DBScheduler
	tasksSvc := task.NewService(repos.Tasks, task.QueueFunc{
		Enq: func(ctx context.Context, t *store.Task) error { return sched.EnqueueForTask(ctx, t) },
		Can: func(ctx context.Context, taskID string) error { return sched.CancelForTask(ctx, taskID) },
	}, repos.Audit, nil)
	sched = scheduler.New(repos, policy, dispatcher, tasksSvc, cfg.Scheduler.TickInterval, cfg.Digest.Time, nil)

	intentsSvc := intent.NewService(repos.Intents, repos.Audit, nil)
	hub := device.NewHub()
	deviceGuard := device.NewGuard(device.DefaultGuardConfig())
	svcs := &agent.ToolServices{
		Memory:      mem,
		Tasks:       tasksSvc,
		Intents:     intentsSvc,
		Weather:     weather,
		Search:      webClient,
		Fetch:       webClient,
		Workspace:   dataDir,
		Guard:       guard,
		Repos:       repos,
		Now:         time.Now,
		Th:          th,
		Device:      hub,
		DeviceGuard: deviceGuard,
	}
	userFn := func(ctx context.Context, userID string) agent.UserContext {
		u, err := repos.Users.Get(ctx, userID)
		if err != nil {
			return agent.UserContext{UserID: userID, TZ: "Asia/Shanghai"}
		}
		return agent.UserContext{UserID: u.ID, UserName: u.Name, UserType: u.UserType, TZ: u.TZ}
	}
	// 生活管家 → 工作助手：手机侧中继客户端（配对后把指令交给电脑执行）。
	phoneRelay := &relay.Phone{StatePath: filepath.Join(dataDir, "relay-phone.json")}
	if err := phoneRelay.Load(); err != nil {
		log.Printf("[gosvc] 读取手机配对失败（需要重新配对）: %v", err)
	}

	// 手机端的采集渠道工具走中继命令，由电脑上的真实采集管理器执行。
	// 手机不注册桌面工具，也不在本机保存渠道配置。
	tools := agent.DefaultTools()
	tools = append(tools, agent.RemoteCollectionTools(&remoteCollection{phone: phoneRelay})...)

	runtime := &agent.Runtime{
		LLM:      llmClient,
		Model:    cfg.LLM.Model,
		Registry: agent.NewRegistry(tools...),
		Prompt:   &agent.PromptBuilder{Memory: mem, UserFn: userFn},
		Tools:    svcs,
		Clock:    agent.SystemClock{},
	}

	webDir := filepath.Join(dataDir, "web")
	if err := ensureWeb(webDir); err != nil {
		return "", err
	}

	api := &httpapi.Server{
		PhoneRelay: phoneRelay,
		// 手机是电脑控制面的远程视图：任务、Worker 会话、审批、交付物与
		// 每日简报都从电脑取，本机不另起一套 Worker，避免双事实源。
		Remote:       phoneRelay,
		Inbox:        inboxSvc,
		Repos:        repos,
		Runtime:      runtime,
		WebDir:       webDir,
		Now:          time.Now,
		Outbox:       outbox,
		Settings:     st,
		RemoteMemory: remoteMemory,
		Delivery:     deliveryClient,
		// 记忆服务：手机记录的联系人事实与备注要进共享长期记忆，
		// 电脑端才能从同一份记忆里检索到。
		Memory:      mem,
		FallbackLLM: settings.LLM{BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model},
		Device:      hub,
		DeviceGuard: deviceGuard,
		// 本机设备标识：会话已读游标按设备分别记录，
		// 手机读过的会话不会把电脑的角标一起清掉。
		DeviceID: devID,
		// 语音口令判定：唤醒词、10s 指令窗、4s 片段合并、30s 上下文全在 Go。
		Voice: voice.NewService(voice.Config{}, time.Now),
	}

	ctx, cancel := context.WithCancel(context.Background())
	if remoteMemory != nil {
		go remoteMemory.Start(ctx)
	} else if nativeMemory != nil {
		go nativeMemory.Start(ctx)
	}
	// 通知中心：每 30s 扫描到期 snooze 并推入 outbox（手机端轮询转系统通知）。
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				items, err := inboxSvc.Due(ctx, now)
				if err != nil {
					log.Printf("[gosvc] inbox due: %v", err)
					continue
				}
				for _, it := range items {
					outbox.PushRoute(it.OwnerUserID, it.Body, httpapi.OutboxRouteInbox, now)
				}
			}
		}
	}()
	go func() {
		if err := sched.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[gosvc] scheduler stopped: %v", err)
		}
	}()
	// 电脑恢复在线后重放排队中的离线指令。
	// 每条指令复用原来的幂等键，电脑端保证最多执行一次。
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				flushRemotePending(ctx, api, repos)
			}
		}
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		return "", fmt.Errorf("gosvc: 监听失败: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	srv := &http.Server{
		Handler:           tokenMiddleware(token, api.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[gosvc] http serve: %v", err)
		}
	}()

	running = &serverState{srv: srv, db: db, hub: hub, cancel: cancel, phone: phoneRelay}
	raw, _ := json.Marshal(map[string]string{"addr": addr, "token": token, "device_id": devID})
	log.Printf("[gosvc] started addr=%s token=%s… device_id=%s", addr, token[:8], devID)
	return string(raw), nil
}

// flushRemotePending 在电脑重新在线后重放排队中的离线指令。
// 没有排队指令或电脑仍离线时安静跳过，不刷日志。
func flushRemotePending(ctx context.Context, api *httpapi.Server, repos *store.Repos) {
	users, err := repos.Users.ListAll(ctx)
	if err != nil || len(users) == 0 {
		return
	}
	count, err := repos.RemotePending.CountPending(ctx, users[0].ID)
	if err != nil || count == 0 {
		return
	}
	replayed, failed, err := api.FlushRemote(ctx, users[0].ID)
	if err != nil {
		return
	}
	if replayed > 0 || failed > 0 {
		log.Printf("[gosvc] 离线指令重放：成功 %d 条，失败 %d 条", replayed, failed)
	}
}

// StopServer 停止服务并释放资源（幂等）。
func StopServer() {
	stateMu.Lock()
	defer stateMu.Unlock()
	if running == nil {
		return
	}
	st := running
	running = nil
	st.cancel()
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = st.srv.Shutdown(shCtx)
	if st.phone != nil {
		st.phone.Close()
	}
	st.hub.Close()
	st.db.Close()
	log.Printf("[gosvc] stopped")
}

// tokenMiddleware 同时校验 X-Token 请求头与 URL query token（WebView 用 query，
// 无障碍 WS 握手 /api/* 用请求头；两者都接受）。
func tokenMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Token")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got == "" || got != token {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ensureSecret 读/生成并持久化一个随机密钥（token 或 device_id）。
func ensureSecret(dataDir, name string, nBytes int) (string, error) {
	p := filepath.Join(dataDir, name)
	if raw, err := os.ReadFile(p); err == nil {
		if v := strings.TrimSpace(string(raw)); len(v) >= nBytes*2 {
			return v, nil
		}
	}
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gosvc: 生成 %s: %w", name, err)
	}
	v := hex.EncodeToString(b)
	if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
		return "", fmt.Errorf("gosvc: 保存 %s: %w", name, err)
	}
	return v, nil
}

// ensureWeb 把内嵌的 Web 资源落到 dataDir/web。
// 页面内容随应用更新：先写临时目录再整体替换，避免旧版本页面长期驻留。
func ensureWeb(dir string) error {
	tmp := dir + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("gosvc: 清理临时 web 目录: %w", err)
	}
	err := fs.WalkDir(webAssets, "webassets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel("webassets", path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := webAssets.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return fmt.Errorf("gosvc: 展开 web 资源: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("gosvc: 替换 web 目录: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("gosvc: 提交 web 目录: %w", err)
	}
	return nil
}

// ensureConfig 首次启动写默认 config.yaml（key 留空走设置页/settings.json）。
func ensureConfig(dataDir string) (*config.Config, error) {
	p := filepath.Join(dataDir, "config.yaml")
	if _, err := os.Stat(p); os.IsNotExist(err) {
		if err := os.WriteFile(p, []byte(defaultConfig(dataDir)), 0o600); err != nil {
			return nil, fmt.Errorf("gosvc: 写默认配置: %w", err)
		}
	}
	return config.Load(p)
}

func defaultConfig(dataDir string) string {
	web := filepath.ToSlash(filepath.Join(dataDir, "web"))
	dbf := filepath.ToSlash(filepath.Join(dataDir, "gfa.db"))
	return fmt.Sprintf(`server:
  addr: "127.0.0.1:0"
  web_dir: %q

database:
  path: %q

llm:
  base_url: "https://api.deepseek.com"
  api_key: ""
  model: "deepseek-flash"
  temperature: 0.4
  max_tokens: 2048

embedder:
  base_url: "http://120.26.102.114:11434/v1"
  api_key: ""
  model: "bge-m3"
  dim: 1024

# 联网搜索/抓取复用上面的 DeepSeek Key（官方 /v1/tools/search、/v1/tools/fetch）。
# base_url 仍保留以兼容旧配置，但内部固定使用 llm.base_url。
search:
  count: 8

thresholds:
  task_auto: 0.85
  task_clarify: 0.6
  person_clarify: 0.8
  fact_confirmed: 0.8

dnd:
  window: "22:00-07:00"
  child_night_silence: true
  urgent_breaks_dnd_for: ["elder", "general"]

scheduler:
  tick_interval: "30s"
  ack_timeout: "30m"
  max_level: 3
  max_daily_push: 3

digest:
  time: "11:00"

memory:
  context_token_budget: 1500
  recency_half_life: "720h"
`, web, dbf)
}
