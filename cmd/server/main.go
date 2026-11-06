// Command server 组装陪伴型 AI 管家：config → store → llm →
// 领域服务 → agent 运行时 → HTTP API，并启动调度器。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/agenthub"
	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/config"
	"goldenfinger/agent/internal/deliveryclient"
	"goldenfinger/agent/internal/device"
	emailsvc "goldenfinger/agent/internal/email"
	"goldenfinger/agent/internal/extsvc"
	"goldenfinger/agent/internal/extsvc/deepseek"
	"goldenfinger/agent/internal/extsvc/stub"
	"goldenfinger/agent/internal/httpapi"
	"goldenfinger/agent/internal/inbox"
	"goldenfinger/agent/internal/intent"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/llm/openai"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/relay"
	"goldenfinger/agent/internal/scheduler"
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/skill"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
	"goldenfinger/agent/internal/task"
	"goldenfinger/agent/internal/voice"
	"goldenfinger/agent/internal/watchdog"
	"goldenfinger/agent/internal/worktask"
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

	// ---- 运行时设置（尽早打开：长期记忆需要在启动时选择 Native / Remote） ----
	settingsPath := os.Getenv("GFA_SETTINGS")
	if settingsPath == "" {
		settingsPath = "settings.json"
	}
	st, err := settings.Open(settingsPath)
	if err != nil {
		fatal(err)
	}

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
				outbox.PushRoute(u.ID, body, httpapi.OutboxRouteTasks, time.Now())
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
	// 共享记忆槽位：记录每个位置当前对应哪条长期记忆来源，内容更新时据此清理旧版本。
	mem.SetSharedSlots(repos.SharedSlots)
	remoteMemory := setupLongTermMemory(ctx, mem, st, cfg, repos, llmClient, embedder)

	// 通知中心：watch 命中经 Notify 复用 Outbox（Web/NotifyPoller 轮询）。
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
	agentHub := agenthub.New(workspace, filepath.Join(filepath.Dir(settingsPath), "agents.json"))

	// 持久化异步任务队列：提交即返回 task_id，执行结果与通知落库，
	// 长任务不再被一次 HTTP 请求的超时拖死（手机端也能重连查询/取消）。
	jobQueue := agenthub.NewDispatcher(db, agentHub)

	collectionManager := collection.New(repos, st)
	tools := append(agent.DefaultTools(), agent.SystemTools()...)
	tools = append(tools, agent.SkillTools(skillPool)...)
	tools = append(tools, agent.WeChatTools(repos)...)
	tools = append(tools, agent.CollectionTools(collectionManager)...)
	tools = append(tools, agenthub.Tools(agentHub)...)
	// 同名覆盖：delegate_agent 改为异步派发，并补充查询/取消工具。
	tools = append(tools, agenthub.AsyncTools(jobQueue)...)
	runtime.Registry = agent.NewRegistry(tools...)

	// Super-agent 事件层：只负责 Trigger/Thread/EventLog 与路由落库，
	// 外部发送仍必须经过 ActionOutbox 人工审批。
	superAgent := superagent.New(db)
	artifactRoot := filepath.Join(filepath.Dir(cfg.Database.Path), "artifacts")
	workTasks := worktask.New(db)
	workTasks.ArtifactRoot = artifactRoot
	artifactSvc := worktask.NewArtifactService(db, workTasks, artifactRoot)
	artifactSvc.Workspace = workspace
	svcs.Artifacts = artifactSvc
	// 异步 Agent 返回的文件产物统一转为 WorkTask 交付物。
	jobQueue.Artifacts = artifactSvc
	jobQueue.Tasks = workTasks
	if err := jobQueue.Start(ctx); err != nil {
		log.Printf("agent-jobs: 队列启动失败（异步派发不可用）: %v", err)
	}

	// 跨端文件交付：与共享记忆共用云端地址/设备 Token，但可单独关闭。
	var deliveryClient *deliveryclient.Client
	if cloud := st.CloudMemory(); cloud.DeliveryEnabled &&
		(cloud.Enabled || os.Getenv("GFA_CLOUD_URL") != "") {
		base, token := cloudConnection(st)
		if base != "" && token != "" {
			client, clientErr := deliveryclient.New(deliveryclient.Config{
				BaseURL: base, Token: token, DeviceID: cloud.DeviceID,
			})
			if clientErr != nil {
				log.Printf("delivery: 客户端初始化失败，文件交付不可用: %v", clientErr)
			} else {
				ownerID, _ := ensureLocalUserID(ctx, repos)
				worker := deliveryclient.NewWorker(deliveryclient.WorkerConfig{
					DB: db, Client: client, Artifacts: artifactSvc, OwnerID: ownerID,
					OnDelivered: func(ctx context.Context, entry *store.DeliveryOutboxEntry, delivery *deliveryclient.Delivery) error {
						text := fmt.Sprintf("%s，电脑把 Artifact %s v%d 交付到 %s（%s，sha256:%s），"+
							"已上传到云端临时交付区，等待目标设备接收。",
							time.Now().Format("2006-01-02"), entry.ArtifactID, entry.ArtifactVersion,
							entry.TargetDeviceID, delivery.Name, delivery.SHA256)
						_, err := mem.SaveSystemEvent(ctx, ownerID, text, "delivery:"+delivery.DeliveryID)
						return err
					},
				})
				svcs.Delivery = worker
				deliveryClient = client
				go worker.Start(ctx)
				log.Printf("delivery: 云端文件交付 enabled (base=%s device=%s)", base, cloud.DeviceID)
			}
		}
	}
	superAgent.Tasks = workTasks
	superAgent.SummaryAllowed = func(ctx context.Context, ownerID, channel string) (bool, error) {
		if !store.IsGatedChannel(store.NormalizeChannel(channel)) {
			return true, nil
		}
		return repos.ChannelConfigs.SummaryEnabled(ctx, ownerID, channel)
	}
	superAgent.AutoWorkerAllowed = func(ctx context.Context, ownerID, channel string) (bool, error) {
		global, err := repos.GlobalConfigs.Get(ctx, ownerID)
		if err != nil || !global.WorkerEnabled {
			return false, err
		}
		if !store.IsGatedChannel(store.NormalizeChannel(channel)) {
			return true, nil
		}
		return repos.ChannelConfigs.AutoWorkerEnabled(ctx, ownerID, channel)
	}
	superAgent.NotifyAllowed = func(ctx context.Context, ownerID string) (bool, error) {
		global, err := repos.GlobalConfigs.Get(ctx, ownerID)
		return global.ProactiveNotifyEnabled, err
	}
	superAgent.TriggerSummaryAllowed = func(ctx context.Context, ownerID string, trigger *store.Trigger) (bool, error) {
		if trigger.Channel != store.ChannelWeb {
			if !store.IsGatedChannel(store.NormalizeChannel(trigger.Channel)) {
				return true, nil
			}
			return repos.ChannelConfigs.SummaryEnabled(ctx, ownerID, trigger.Channel)
		}
		site, ok := st.Site(superagent.WebSiteID(trigger))
		return ok && site.Enabled && site.SummaryEnabled, nil
	}
	superAgent.TriggerAutoWorkerAllowed = func(ctx context.Context, ownerID string, trigger *store.Trigger) (bool, error) {
		global, err := repos.GlobalConfigs.Get(ctx, ownerID)
		if err != nil || !global.WorkerEnabled {
			return false, err
		}
		if trigger.Channel != store.ChannelWeb {
			if !store.IsGatedChannel(store.NormalizeChannel(trigger.Channel)) {
				return true, nil
			}
			return repos.ChannelConfigs.AutoWorkerEnabled(ctx, ownerID, trigger.Channel)
		}
		site, ok := st.Site(superagent.WebSiteID(trigger))
		return ok && site.Enabled && site.AutoWorkerEnabled, nil
	}
	superAgent.OnNotify = func(_ context.Context, ownerID string, trigger *store.Trigger, decision superagent.Decision) {
		outbox.PushRoute(ownerID, notifyText(trigger, decision), httpapi.OutboxRouteTasks, time.Now())
	}

	api := &httpapi.Server{
		Repos:           repos,
		Runtime:         runtime,
		WebDir:          cfg.Server.WebDir,
		UploadDir:       filepath.Join(filepath.Dir(cfg.Database.Path), "uploads"),
		Skills:          skillPool,
		Now:             time.Now,
		Outbox:          outbox,
		Settings:        st,
		Collection:      collectionManager,
		FallbackLLM:     settings.LLM{BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model},
		Device:          deviceHub,
		DeviceGuard:     deviceGuard,
		Inbox:           inboxSvc,
		Agents:          agentHub,
		Jobs:            jobQueue,
		AgentDispatcher: jobQueue,
		// 语音口令判定（唤醒词/指令窗口/片段合并）在 Go，手机只执行动作。
		Voice:        voice.NewService(voice.Config{}, time.Now),
		SuperAgent:   superAgent,
		Tasks:        workTasks,
		Artifacts:    artifactSvc,
		RemoteMemory: remoteMemory,
		Delivery:     deliveryClient,
		Coordinator:  &superagent.Coordinator{Tasks: workTasks},
		// 记忆服务：联系人备注、共享记忆写入都要经过它，
		// 否则只写本机 SQLite，另一端检索不到。
		Memory: mem,
		// 桌面本机界面也是一个「设备」：已读游标按设备分别记录，
		// 不能让桌面和手机的角标互相清掉。
		DeviceID: "desktop",
	}

	llmFor := func() (llm.Client, string) {
		rt := api.EffectiveRuntime()
		if rt == nil || rt.LLM == nil {
			return nil, ""
		}
		return rt.LLM, rt.Model
	}
	superAgent.Summarizer = superagent.NewSummarizer(db, llmFor)
	briefs := superagent.NewBriefGenerator(db, llmFor)
	// 简报正文写进共享长期记忆：手机与电脑只共享这一份记忆。
	briefs.SharedMemory = mem
	briefs.ChannelEnabled = func(ctx context.Context, ownerID, channel string) (bool, error) {
		if channel == store.ChannelWeb {
			return true, nil
		}
		if !store.IsGatedChannel(store.NormalizeChannel(channel)) {
			return true, nil
		}
		return repos.ChannelConfigs.BriefEnabled(ctx, ownerID, channel)
	}
	briefs.TriggerEnabled = func(_ context.Context, _ string, trigger *store.Trigger) bool {
		if trigger.Channel != store.ChannelWeb {
			return true
		}
		site, ok := st.Site(superagent.WebSiteID(trigger))
		return ok && site.Enabled && site.BriefEnabled
	}
	briefs.ThreadEnabled = func(_ context.Context, _ string, thread *store.Thread) bool {
		if thread.Channel != store.ChannelWeb {
			return true
		}
		parts := strings.SplitN(thread.ExternalID, ":", 4)
		if len(parts) < 2 {
			return false
		}
		site, ok := st.Site(parts[1])
		return ok && site.Enabled && site.BriefEnabled
	}
	briefs.OnReady = func(ctx context.Context, ownerID string, brief *store.DailyBrief) {
		global, err := repos.GlobalConfigs.Get(ctx, ownerID)
		if err != nil || !global.ProactiveNotifyEnabled {
			return
		}
		outbox.PushRoute(ownerID, "今日简报已生成（第 "+strconv.Itoa(brief.Version)+" 版）："+truncateText(brief.ContentMD, 500), httpapi.OutboxRouteBrief, time.Now())
	}
	api.Briefs = briefs

	// 可选 IMAP 收件：只读正文，不标记已读、不发送邮件；关闭时完全空转。
	emailOwnerID, err := ensureLocalUserID(ctx, repos)
	if err != nil {
		log.Printf("[email] 无法准备本地用户，IMAP 采集未启动：%v", err)
	} else {
		emailPoller := emailsvc.NewPoller(func() emailsvc.Config {
			e := st.Email()
			// 渠道关闭时在采集端停止，不建立 IMAP 连接，也不把正文交给服务端。
			channelOn, _ := repos.ChannelConfigs.Enabled(context.Background(), emailOwnerID, store.ChannelEmail)
			return emailsvc.Config{
				Enabled: e.Enabled && channelOn, Host: e.Host, Port: e.Port, Username: e.Username,
				Password: e.Password, Mailbox: e.Mailbox, Mailboxes: e.Mailboxes,
				TLS: e.TLS, InitialFetch: e.InitialFetch,
				PollInterval:   time.Duration(e.PollSeconds) * time.Second,
				Senders:        e.Senders,
				SubjectFilters: e.SubjectFilters,
				SinceDays:      e.SinceDays,
			}
		}, &emailCursorStore{db: db, ownerID: emailOwnerID}, func(ctx context.Context, message emailsvc.Message) error {
			enabled, err := repos.ChannelConfigs.Enabled(ctx, emailOwnerID, store.ChannelEmail)
			if err != nil {
				return err
			}
			if !enabled {
				return repos.ChannelSkips.Increment(ctx, emailOwnerID, store.ChannelEmail,
					time.Now().In(time.Local).Format("2006-01-02"), time.Now())
			}
			allowed, err := repos.ChannelPolicies.Allowed(ctx, emailOwnerID, store.ChannelEmail, message.Account)
			if err != nil {
				return err
			}
			if !allowed {
				return repos.ChannelSkips.Increment(ctx, emailOwnerID, store.ChannelEmail,
					time.Now().In(time.Local).Format("2006-01-02"), time.Now())
			}
			payload, _ := json.Marshal(map[string]any{
				"account": message.Account, "mailbox": message.Mailbox,
				"from": message.From, "to": message.To, "cc": message.Cc,
				"snippet": message.Snippet, "attachments": message.Attachments,
				"raw_ref": fmt.Sprintf("imap:%s:%d", message.Mailbox, message.UID),
			})
			title := strings.TrimSpace(message.Subject)
			if title == "" {
				title = message.Snippet
			}
			_, err = superAgent.Ingest(ctx, emailOwnerID, superagent.TriggerInput{
				Channel: store.ChannelEmail, Kind: "email_message",
				ExternalID:       "email:" + message.Account + ":" + message.Mailbox + ":" + message.MessageID,
				ThreadExternalID: "email:" + message.Account + ":" + message.ThreadKey,
				Title:            title,
				Body:             firstText(message.Text, message.Snippet),
				Payload:          payload,
				ReceivedAt:       message.ReceivedAt,
			})
			return err
		})
		go emailPoller.Run(ctx)
	}

	// Super-agent Worker 复用 TurnService；create_worker 只有在执行器启动成功时
	// 才挂到 WorkerSink，否则 Router 会按确定性回退降级为通知。
	turnService := &agent.TurnService{
		Repos:        repos,
		RuntimeFor:   api.EffectiveRuntime,
		HistoryLimit: api.HistoryLimit,
		UploadDir:    api.UploadDir,
	}
	workerManager := superagent.NewWorkerManager(db, superAgentWorkerRunner{turns: turnService})
	workerManager.Tasks = workTasks
	workerManager.Artifacts = artifactSvc
	contextBroker := superagent.NewContextBroker(repos, mem)
	contextBroker.Now = time.Now
	workerManager.Context = func(ctx context.Context, job *store.AgentJob) ([]string, json.RawMessage, error) {
		bundle, err := contextBroker.Build(ctx, job.OwnerUserID, job.WorkTaskID, job.ID)
		if err != nil {
			return nil, nil, err
		}
		receipts, err := json.Marshal(bundle.Receipts)
		if err != nil {
			return bundle.Blocks, nil, nil
		}
		return bundle.Blocks, receipts, nil
	}
	workTasks.OnEnqueue = func(_ context.Context, _, _, _ string) {
		workerManager.Wake()
	}
	workTasks.OnCancel = func(ctx context.Context, ownerID, taskID, jobID string) {
		workerManager.CancelJob(ctx, ownerID, taskID, jobID)
	}
	workTasks.DeviceRecoveryCheck = func(ctx context.Context, ownerID string, task *store.WorkTask) (bool, error) {
		bindings, err := repos.Devices.TaskDevices(ctx, ownerID, task.ID)
		if err != nil {
			return false, err
		}
		if len(bindings) == 0 {
			return true, nil
		}
		for _, binding := range bindings {
			device, err := repos.Devices.Get(ctx, ownerID, binding.DeviceID)
			if err != nil || device.RevokedAt != nil || !device.Online {
				continue
			}
			return true, nil
		}
		return false, nil
	}
	// Worker 的结论进入统一记忆（episode + 长期记忆镜像），主会话可检索追问；
	// 原始 Worker 会话仍保留在 agent_job / chat_session，不进入用户可见会话列表。
	workerManager.OnResult = func(ctx context.Context, job *store.AgentJob, summary string) error {
		text := "Worker 任务结果\n任务：" + job.Task + "\n结果：" + summary
		_, err := mem.SaveWorkerResult(ctx, job.OwnerUserID, text, "worker:"+job.ID)
		return err
	}
	if raw := os.Getenv("GFA_SUPERAGENT_MAX_WORKERS"); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil && n > 0 && n <= 8 {
			workerManager.MaxWorkers = n
		}
	}
	if err := workerManager.Start(ctx); err != nil {
		log.Printf("superagent-workers: 启动失败（create_worker 将降级为通知）: %v", err)
	} else {
		superAgent.Workers = workerManager
		api.SuperAgentWorkers = workerManager
		log.Printf("superagent-workers: started (max=%d)", workerManager.MaxWorkers)
	}

	watchdogSvc := watchdog.New(db, workTasks)
	watchdogSvc.OnNotify = func(ownerID, body string) {
		outbox.PushRoute(ownerID, body, httpapi.OutboxRouteTasks, time.Now())
	}
	go watchdogSvc.Start(ctx)
	log.Printf("watchdog: started (interval=%s stale=%s)", watchdogSvc.Interval, watchdogSvc.StaleAfter)

	// Router 后台批处理默认关闭；GFA_SUPERAGENT=1 时每 2s 处理一批 queued Trigger。
	if os.Getenv("GFA_SUPERAGENT") == "1" {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					var router superagent.Router = superagent.DeterministicRouter{}
					if rt := api.EffectiveRuntime(); rt != nil && rt.LLM != nil &&
						os.Getenv("GFA_SUPERAGENT_ROUTER") != "deterministic" {
						router = &superagent.LLMRouter{Client: rt.LLM, Model: rt.Model, TaskIndex: superAgent.TaskIndex}
					}
					users, err := repos.Users.ListAll(ctx)
					if err != nil {
						if ctx.Err() == nil {
							log.Printf("superagent router: %v", err)
						}
						continue
					}
					routed := 0
					for _, user := range users {
						global, gErr := repos.GlobalConfigs.Get(ctx, user.ID)
						if gErr != nil || !global.RouterEnabled {
							continue
						}
						results, rErr := superAgent.RoutePending(ctx, user.ID, router, 20)
						if rErr != nil {
							if ctx.Err() == nil {
								log.Printf("superagent router: %v", rErr)
							}
							continue
						}
						routed += len(results)
					}
					if routed > 0 {
						log.Printf("superagent router: routed %d trigger(s)", routed)
					}
				}
			}
		}()
		log.Printf("superagent router: enabled (batch=2s, llm fallback=deterministic)")
	}

	// 每日简报：到达 digest.time 后生成当天第一版；之后有新事件时最多每 30 分钟再出更新版。
	if strings.TrimSpace(cfg.Digest.Time) != "" {
		go runBriefScheduler(ctx, briefs, repos, cfg.Digest.Time)
	}

	// ---- 手机 → 电脑：中继接收器（仅 GFA_RELAY_URL 配置时启用）----
	// 电脑端只负责“被手机唤起执行”，不主动向手机推送。
	if relayURL := os.Getenv("GFA_RELAY_URL"); relayURL != "" {
		phoneRelay := &relay.Desktop{
			BaseURL:   relayURL,
			Name:      envOrDefault("GFA_RELAY_NAME", hostname()),
			StatePath: filepath.Join(filepath.Dir(settingsPath), "relay.json"),
			Handle:    relay.ExecuteLocal(localAPIBase(cfg.Server.Addr), nil),
			// 持久化指令回执：电脑重启后重投的指令不会执行第二遍。
			Ledger: repos.RelayReceipts,
			// v2 端会同时收到任务当前 Artifact 引用；文件正文仍由云端收件箱下载。
			Enrich: func(ctx context.Context, payload *relay.ResultPayload) {
				if payload == nil || payload.TaskID == "" {
					return
				}
				ownerID, ownerErr := ensureLocalUserID(ctx, repos)
				if ownerErr != nil {
					return
				}
				job, jobErr := repos.AgentJobs.Get(ctx, ownerID, payload.TaskID)
				if jobErr != nil || job.WorkTaskID == "" {
					return
				}
				artifacts, listErr := repos.Artifacts.ListByTask(ctx, ownerID, job.WorkTaskID)
				if listErr != nil {
					return
				}
				for _, artifact := range artifacts {
					if artifact.CurrentVersion <= 0 {
						continue
					}
					version, getErr := repos.ArtifactVersions.Get(ctx, ownerID, artifact.ID, artifact.CurrentVersion)
					if getErr != nil {
						continue
					}
					payload.Artifacts = append(payload.Artifacts, relay.ArtifactRef{
						ArtifactID: artifact.ID, Version: version.Version,
						Name: firstText(version.OriginalName, artifact.Title),
						MIME: version.MimeType, Size: version.SizeBytes, SHA256: version.ContentHash,
					})
				}
			},
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
		// 回执只用来保证「重投不重复执行」，过期后清理，避免表无限增长。
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := repos.RelayReceipts.Prune(ctx, time.Now().Add(-30*24*time.Hour)); err != nil {
						log.Printf("relay: 清理过期回执失败: %v", err)
					}
				}
			}
		}()
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
					outbox.PushRoute(it.OwnerUserID, it.Body, httpapi.OutboxRouteInbox, now)
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

func notifyText(trigger *store.Trigger, decision superagent.Decision) string {
	for _, candidate := range []string{decision.Reply, decision.Question, trigger.Title, trigger.Body, decision.Reason} {
		if strings.TrimSpace(candidate) != "" {
			return truncateText(candidate, 500)
		}
	}
	return "新事件：" + trigger.Channel
}

func truncateText(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

type superAgentWorkerRunner struct {
	turns *agent.TurnService
}

func (r superAgentWorkerRunner) RunWorker(ctx context.Context, req superagent.WorkerRequest) (*superagent.WorkerResult, error) {
	var onEvent func(agent.Event)
	if req.OnEvent != nil {
		onEvent = func(ev agent.Event) {
			req.OnEvent(superagent.WorkerEvent{Kind: ev.Kind, Name: ev.Name, Detail: ev.Detail})
		}
	}
	out, err := r.turns.Run(ctx, agent.TurnRequest{
		UserID:         req.OwnerID,
		SessionID:      req.SessionID,
		Text:           req.Goal,
		Kind:           "worker",
		ThreadID:       req.ThreadID,
		WorkTaskID:     req.TaskID,
		JobID:          req.JobID,
		StepController: req.StepController,
		ContextBlocks:  req.ContextBlocks,
		ToolProfile:    req.Profile,
		OnEvent:        onEvent,
	})
	if err != nil {
		return nil, err
	}
	result := &superagent.WorkerResult{
		SessionID: out.SessionID,
		Reply:     out.Reply,
		Summary:   truncateText(out.Reply, 4000),
		Paused:    out.Paused,
		Cancelled: out.Cancelled,
	}
	if out.Pending != nil {
		result.WaitingKind = "user"
		if raw, marshalErr := json.Marshal(out.Pending); marshalErr == nil {
			result.WaitingPayload = raw
		}
	}
	return result, nil
}

// runBriefScheduler 在每天 digest.time 后生成首版简报；有新事件时最多每 30 分钟补一版。
func runBriefScheduler(ctx context.Context, gen *superagent.BriefGenerator, repos *store.Repos, hhmm string) {
	targetClock, err := time.ParseInLocation("15:04", strings.TrimSpace(hhmm), time.Local)
	if err != nil {
		log.Printf("brief scheduler: invalid digest.time %q: %v", hhmm, err)
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			local := now.In(time.Local)
			target := time.Date(local.Year(), local.Month(), local.Day(),
				targetClock.Hour(), targetClock.Minute(), 0, 0, time.Local)
			if local.Before(target) {
				continue
			}
			users, err := repos.Users.ListAll(ctx)
			if err != nil {
				log.Printf("brief scheduler: list users: %v", err)
				continue
			}
			date := local.Format("2006-01-02")
			start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
			end := start.AddDate(0, 0, 1)
			for _, user := range users {
				global, gErr := repos.GlobalConfigs.Get(ctx, user.ID)
				if gErr != nil || !global.DailyBriefEnabled {
					continue
				}
				latest, latestErr := repos.DailyBriefs.Latest(ctx, user.ID, date)
				if latestErr != nil && !errors.Is(latestErr, store.ErrNotFound) {
					log.Printf("brief scheduler: latest %s: %v", user.ID, latestErr)
					continue
				}
				need := latest == nil
				if latest != nil {
					if local.Sub(latest.UpdatedAt) < 30*time.Minute {
						continue
					}
					triggers, listErr := repos.Triggers.ListBetween(ctx, user.ID, "", start, end, 1000)
					if listErr != nil {
						log.Printf("brief scheduler: triggers %s: %v", user.ID, listErr)
						continue
					}
					for i := range triggers {
						trigger := &triggers[i]
						if trigger.ReceivedAt.After(latest.UpdatedAt) &&
							gen.IncludesTrigger(ctx, user.ID, trigger) {
							need = true
							break
						}
					}
				}
				if !need {
					continue
				}
				genCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				brief, genErr := gen.Generate(genCtx, user.ID, local)
				cancel()
				if genErr != nil {
					log.Printf("brief scheduler: generate %s: %v", user.ID, genErr)
					continue
				}
				log.Printf("brief scheduler: generated %s v%d", brief.BriefDate, brief.Version)
			}
		}
	}
}

type emailCursorStore struct {
	db      *store.DB
	ownerID string
}

func (c *emailCursorStore) Get(ctx context.Context, account string) (emailsvc.Cursor, error) {
	value, err := store.NewRepos(c.db).ChannelCursors.Get(ctx, c.ownerID, store.ChannelEmail, account)
	if err != nil || value == "" {
		return emailsvc.Cursor{}, err
	}
	var cursor emailsvc.Cursor
	if err := json.Unmarshal([]byte(value), &cursor); err == nil {
		return cursor, nil
	}
	// 兼容旧版本只保存 UID 的游标。
	n, legacyErr := strconv.ParseUint(value, 10, 32)
	if legacyErr != nil {
		return emailsvc.Cursor{}, legacyErr
	}
	return emailsvc.Cursor{UID: uint32(n)}, nil
}

func (c *emailCursorStore) Set(ctx context.Context, account string, cursor emailsvc.Cursor) error {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	return store.NewRepos(c.db).ChannelCursors.Set(ctx, c.ownerID, store.ChannelEmail, account,
		string(raw), time.Now())
}

func ensureLocalUserID(ctx context.Context, repos *store.Repos) (string, error) {
	users, err := repos.Users.ListAll(ctx)
	if err != nil {
		return "", err
	}
	if len(users) > 0 {
		return users[0].ID, nil
	}
	u := &store.User{UserType: store.UserGeneral, Name: "演示用户", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		return "", err
	}
	return u.ID, nil
}

// setupLongTermMemory 在启动时确定唯一长期记忆主后端：
// 云端模式只使用 RemoteMemory，不再同时运行本地 Native 双写。
func setupLongTermMemory(ctx context.Context, mem *memory.Service, st *settings.Store,
	cfg *config.Config, repos *store.Repos, llmClient llm.Client, embedder llm.Embedder) *memory.Remote {
	if os.Getenv("GFA_MEMORY_DISABLE") == "1" {
		log.Printf("long-term memory: 已禁用，仅使用 SQLite 检索")
		return nil
	}
	cloud := st.CloudMemory()
	envBase := os.Getenv("GFA_CLOUD_URL")
	baseURL := firstText(envBase, cloud.BaseURL)
	token := firstText(os.Getenv("GFA_CLOUD_TOKEN"), st.CloudToken())
	if cloud.Enabled || envBase != "" {
		if baseURL == "" || token == "" {
			log.Printf("long-term memory: 云端模式缺少 base_url/token，保持本机模式")
		} else {
			ownerID, ownerErr := ensureLocalUserID(ctx, repos)
			if ownerErr != nil {
				log.Printf("long-term memory: 无法确定本地 owner，保持本机模式: %v", ownerErr)
			} else {
				remote, remoteErr := memory.NewRemote(memory.RemoteConfig{
					BaseURL: baseURL, Token: token, DeviceID: cloud.DeviceID,
					OwnerID: ownerID, DB: repos.DB,
				})
				if remoteErr != nil {
					log.Printf("long-term memory: 云端客户端初始化失败，保持本机模式: %v", remoteErr)
				} else {
					mem.SetLongTermMemory(remote)
					go remote.Start(ctx)
					log.Printf("long-term memory: 云端共享模式 enabled (base=%s device=%s owner=%s)",
						baseURL, cloud.DeviceID, ownerID)
					return remote
				}
			}
		}
	}
	if native, nativeErr := memory.NewNative(memory.NativeConfig{
		Repos: repos, LLM: llmClient, Model: cfg.LLM.Model, Embed: embedder,
		RecencyHalfLife: cfg.Memory.RecencyHalfLife,
	}); nativeErr != nil {
		log.Printf("long-term memory: 内置后端初始化失败，降级为 SQLite 检索: %v", nativeErr)
	} else {
		mem.SetLongTermMemory(native)
		go native.Start(ctx)
		log.Printf("long-term memory: built-in sqlite-native provider enabled (no python process)")
	}
	return nil
}

// cloudConnection 解析云端地址与设备 Token（环境变量优先于设置文件）。
func cloudConnection(st *settings.Store) (baseURL, token string) {
	cfg := st.CloudMemory()
	return firstText(os.Getenv("GFA_CLOUD_URL"), cfg.BaseURL),
		firstText(os.Getenv("GFA_CLOUD_TOKEN"), st.CloudToken())
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "server:", err)
	os.Exit(1)
}
