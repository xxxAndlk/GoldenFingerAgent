// Command server 组装陪伴型 AI 管家：config → store → llm →
// 领域服务 → agent 运行时 → HTTP API，并启动调度器。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/agenthub"
	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/config"
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
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/skill"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
	"goldenfinger/agent/internal/voice"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ---- 持久化 ----
	db, err := store.Connect(ctx, cfg.Database.Path)
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	if applied, err := db.Migrate(ctx); err != nil {
		fatal(err)
	} else if len(applied) > 0 {
		log.Printf("migrations applied: %v", applied)
	}
	repos := store.NewRepos(db)

	// ---- 外部服务（接口 + 桩实现；LLM 为真实服务） ----
	llmClient := openai.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	embedder := openai.New(cfg.Embedder.BaseURL, cfg.Embedder.APIKey, cfg.Embedder.Model)
	embedder.EmbedModel = cfg.Embedder.Model

	var weather extsvc.WeatherService = stub.Weather{}

	// Web 搜索与抓取共用当前 DeepSeek API Key，调用官方 /v1/tools 接口。
	webClient := deepseek.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.Search.Count)
	log.Printf("web tools: DeepSeek Search + Fetch (base=%s)", webClient.BaseURL)

	outbox := &httpapi.Outbox{}
	var dispatcher scheduler.Dispatcher = scheduler.DeliverFunc(
		func(ctx context.Context, u *store.User, r *store.Reminder, body string) error {
			switch r.Channel {
			case "push":
				return stub.Push{}.Push(ctx, "device-token", extsvc.PushMessage{Title: "管家提醒", Body: body, Level: r.Level})
			case "sms":
				return stub.SMS{}.Send(ctx, "phone", body)
			default:
				outbox.Push(u.ID, body, time.Now())
			}
			return nil
		})

	// ---- 领域服务 ----
	th := nlu.Thresholds{
		TaskAuto:      cfg.Thresholds.TaskAuto,
		TaskClarify:   cfg.Thresholds.TaskClarify,
		PersonClarify: cfg.Thresholds.PersonClarify,
		FactConfirmed: cfg.Thresholds.FactConfirmed,
	}
	mem := memory.NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit,
		embedder, th, cfg.Memory.RecencyHalfLife, nil)
	// 长期记忆统一使用内置 Go 实现（无外部进程、无新依赖）。
	// GFA_MEMORY_DISABLE=1 时仅使用 SQLite 检索，用于排障。
	if os.Getenv("GFA_MEMORY_DISABLE") == "1" {
		log.Printf("long-term memory: 已禁用，仅使用 SQLite 检索")
	} else if native, err := memory.NewNative(memory.NativeConfig{
		Repos: repos, LLM: llmClient, Model: cfg.LLM.Model, Embed: embedder,
		RecencyHalfLife: cfg.Memory.RecencyHalfLife,
	}); err != nil {
		log.Printf("long-term memory: 内置后端初始化失败，降级为 SQLite 检索: %v", err)
	} else {
		mem.SetLongTermMemory(native)
		go native.Start(ctx)
		log.Printf("long-term memory: built-in sqlite-native provider enabled (no python process)")
	}

	// 通知中心：watch 命中经 Notify 复用 Outbox（Web/NotifyPoller 轮询）。
	inboxSvc := inbox.NewService(repos, embedder, time.Now, cfg.Embedder.Dim)
	inboxSvc.Notify = func(_ context.Context, ownerID, body string) {
		outbox.Push(ownerID, body, time.Now())
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

	// task.Service ↔ scheduler：用函数引用打破构造器循环依赖。
	var sched *scheduler.DBScheduler
	tasksSvc := task.NewService(repos.Tasks, task.QueueFunc{
		Enq: func(ctx context.Context, t *store.Task) error { return sched.EnqueueForTask(ctx, t) },
		Can: func(ctx context.Context, taskID string) error { return sched.CancelForTask(ctx, taskID) },
	}, repos.Audit, nil)
	sched = scheduler.New(repos, policy, dispatcher, tasksSvc, cfg.Scheduler.TickInterval, cfg.Digest.Time, nil)

	// ---- agent 运行时 ----
	workspace, _ := os.Getwd()
	skillPool := skill.NewPool()
	// 开发启动时 cwd 可能是 desktop/server；打包后 skills 会放在 server 目录。
	skillRoots := []string{
		os.Getenv("GFA_SKILLS_DIR"),
		workspace,
		filepath.Dir(cfg.Server.WebDir),
		filepath.Dir(filepath.Dir(cfg.Server.WebDir)),
		filepath.Dir(filepath.Dir(filepath.Dir(cfg.Server.WebDir))),
	}
	for _, root := range skillRoots {
		if root == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(root, "skills")); err != nil || !info.IsDir() {
			continue
		}
		if loaded, skipped := skillPool.LoadDir(root); loaded > 0 || len(skipped) > 0 {
			log.Printf("skills: root=%s loaded=%d skipped=%v", root, loaded, skipped)
		}
		break
	}
	intentsSvc := intent.NewService(repos.Intents, repos.Audit, nil)
	deviceHub := device.NewHub()
	// 安全闸由 HTTP（急停）与工具层（确认）共享同一实例。
	deviceGuard := device.NewGuard(device.DefaultGuardConfig())
	defer deviceHub.Close()
	svcs := &agent.ToolServices{
		Memory:    mem,
		Tasks:     tasksSvc,
		Intents:   intentsSvc,
		Weather:   weather,
		Search:    webClient,
		Fetch:     webClient,
		Workspace: workspace,
		Guard:     guard,
		Repos:     repos,
		Now:       time.Now,
		Th:        th,
		Device:    deviceHub,
		// 设备操作安全闸：敏感页面/内容需人工确认，急停窗口内全部拒绝。
		DeviceGuard: deviceGuard,
	}
	userFn := func(ctx context.Context, userID string) agent.UserContext {
		u, err := repos.Users.Get(ctx, userID)
		if err != nil {
			return agent.UserContext{UserID: userID, TZ: "Asia/Shanghai"}
		}
		return agent.UserContext{UserID: u.ID, UserName: u.Name, UserType: u.UserType, TZ: u.TZ}
	}
	runtime := &agent.Runtime{
		LLM:      llmClient,
		Model:    cfg.LLM.Model,
		Registry: agent.NewRegistry(agent.DefaultTools()...),
		Prompt:   &agent.PromptBuilder{Memory: mem, UserFn: userFn, Skills: skillPool},
		Tools:    svcs,
		Clock:    agent.SystemClock{},
	}

	// ---- HTTP API ----
	settingsPath := os.Getenv("GFA_SETTINGS")
	if settingsPath == "" {
		settingsPath = "settings.json"
	}
	st, err := settings.Open(settingsPath)
	if err != nil {
		fatal(err)
	}
	agentHub := agenthub.New(workspace, filepath.Join(filepath.Dir(settingsPath), "agents.json"))

	// 持久化异步任务队列：提交即返回 task_id，执行结果与通知落库，
	// 长任务不再被一次 HTTP 请求的超时拖死（手机端也能重连查询/取消）。
	jobQueue := agenthub.NewDispatcher(db, agentHub)
	if err := jobQueue.Start(ctx); err != nil {
		log.Printf("agent-jobs: 队列启动失败（异步派发不可用）: %v", err)
	}

	tools := append(agent.DefaultTools(), agent.SystemTools()...)
	tools = append(tools, agent.SkillTools(skillPool)...)
	tools = append(tools, agenthub.Tools(agentHub)...)
	// 同名覆盖：delegate_agent 改为异步派发，并补充查询/取消工具。
	tools = append(tools, agenthub.AsyncTools(jobQueue)...)
	runtime.Registry = agent.NewRegistry(tools...)
	api := &httpapi.Server{
		Repos:       repos,
		Runtime:     runtime,
		WebDir:      cfg.Server.WebDir,
		UploadDir:   filepath.Join(filepath.Dir(cfg.Database.Path), "uploads"),
		Skills:      skillPool,
		Now:         time.Now,
		Outbox:      outbox,
		Settings:    st,
		FallbackLLM: settings.LLM{BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model},
		Device:      deviceHub,
		DeviceGuard: deviceGuard,
		Inbox:       inboxSvc,
		Agents:      agentHub,
		Jobs:        jobQueue,
		// 语音口令判定（唤醒词/指令窗口/片段合并）在 Go，手机只执行动作。
		Voice: voice.NewService(voice.Config{}, time.Now),
	}

	// ---- 手机 → 电脑：中继接收器（仅 GFA_RELAY_URL 配置时启用）----
	// 电脑端只负责“被手机唤起执行”，不主动向手机推送。
	if relayURL := os.Getenv("GFA_RELAY_URL"); relayURL != "" {
		phoneRelay := &relay.Desktop{
			BaseURL:   relayURL,
			Name:      envOrDefault("GFA_RELAY_NAME", hostname()),
			StatePath: filepath.Join(filepath.Dir(settingsPath), "relay.json"),
			Handle:    relay.ExecuteLocal(localAPIBase(cfg.Server.Addr), nil),
		}
		if err := phoneRelay.Load(); err != nil {
			log.Printf("relay: 读取凭据失败（将重新注册）: %v", err)
		}
		api.Relay = phoneRelay
		go func() {
			if err := phoneRelay.Run(ctx); err != nil && ctx.Err() == nil {
				log.Printf("relay: 连接中止: %v", err)
			}
		}()
		log.Printf("relay: 已启用中继 %s（配对码见设置页或 POST /api/relay/pair-code）", relayURL)
	} else {
		log.Printf("relay: 未配置 GFA_RELAY_URL，手机远程控制处于关闭状态")
	}

	// ---- 调度器主循环 ----
	go func() {
		log.Printf("scheduler started (tick=%s)", cfg.Scheduler.TickInterval)
		if err := sched.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("scheduler stopped: %v", err)
		}
	}()

	// 通知中心：每 30s 扫描到期的 snooze，推送 Outbox。
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
					log.Printf("inbox due: %v", err)
					continue
				}
				for _, it := range items {
					outbox.Push(it.OwnerUserID, it.Body, now)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("listening on %s (web dir: %s)", cfg.Server.Addr, cfg.Server.WebDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal(err)
		}
	}()

	<-ctx.Done()
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	fmt.Println("bye")
}

// localAPIBase 把监听地址转成本机可直连的 HTTP 基址。
func localAPIBase(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "http://127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return "http://" + addr
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "电脑"
	}
	return name
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "server:", err)
	os.Exit(1)
}
