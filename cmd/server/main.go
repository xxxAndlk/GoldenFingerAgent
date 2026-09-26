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
	"syscall"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/config"
	"goldenfinger/agent/internal/extsvc"
	"goldenfinger/agent/internal/extsvc/firecrawl"
	"goldenfinger/agent/internal/extsvc/stub"
	"goldenfinger/agent/internal/httpapi"
	"goldenfinger/agent/internal/intent"
	"goldenfinger/agent/internal/llm/openai"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/scheduler"
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ---- 持久化 ----
	db, err := store.Connect(ctx, cfg.Database.URL)
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	if applied, err := db.Migrate(ctx); err != nil {
		fatal(err)
	} else if len(applied) > 0 {
		log.Printf("migrations applied: %v", applied)
	}
	repos := store.NewRepos(db.Pool)

	// ---- 外部服务（接口 + 桩实现；LLM 为真实服务） ----
	llmClient := openai.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	embedder := openai.New(cfg.Embedder.BaseURL, cfg.Embedder.APIKey, cfg.Embedder.Model)
	embedder.EmbedModel = cfg.Embedder.Model

	var weather extsvc.WeatherService = stub.Weather{}

	// Web 搜索：配置了 api_key 时使用真实 Firecrawl 客户端，否则用开发桩。
	var search extsvc.SearchService = stub.Search{}
	if cfg.Search.APIKey != "" {
		search = firecrawl.New(cfg.Search.BaseURL, cfg.Search.APIKey, cfg.Search.Count)
		log.Printf("web search: firecrawl client (base=%s)", cfg.Search.BaseURL)
	} else {
		log.Printf("web search: no api_key, using stub")
	}

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
	intentsSvc := intent.NewService(repos.Intents, repos.Audit, nil)
	svcs := &agent.ToolServices{
		Memory:  mem,
		Tasks:   tasksSvc,
		Intents: intentsSvc,
		Weather: weather,
		Search:  search,
		Guard:   guard,
		Repos:   repos,
		Now:     time.Now,
		Th:      th,
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
		Prompt:   &agent.PromptBuilder{Memory: mem, UserFn: userFn},
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
	api := &httpapi.Server{
		Repos:       repos,
		Runtime:     runtime,
		WebDir:      cfg.Server.WebDir,
		Now:         time.Now,
		Outbox:      outbox,
		Settings:    st,
		FallbackLLM: settings.LLM{BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model},
	}

	// ---- 调度器主循环 ----
	go func() {
		log.Printf("scheduler started (tick=%s)", cfg.Scheduler.TickInterval)
		if err := sched.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("scheduler stopped: %v", err)
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "server:", err)
	os.Exit(1)
}
