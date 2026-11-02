package store

// Repos 在一个 Querier（连接池或事务）之上聚合所有仓储。
type Repos struct {
	Users               *UserRepo
	Persons             *PersonRepo
	Facts               *FactRepo
	Tasks               *TaskRepo
	Reminders           *ReminderRepo
	Episodes            *EpisodeRepo
	Intents             *IntentRepo
	Audit               *AuditRepo
	Consents            *ConsentRepo
	Sessions            *SessionRepo
	InboxEvents         *InboxEventRepo
	InboxSnoozes        *InboxSnoozeRepo
	InboxWatches        *InboxWatchRepo
	AgentJobs           *AgentJobRepo
	AgentNotifications  *AgentNotificationRepo
	LongTerm            *LongTermEdgeRepo
	Threads             *ThreadRepo
	ThreadLinks         *ThreadLinkRepo
	ThreadSubscriptions *ThreadSubscriptionRepo
	Triggers            *TriggerRepo
	ActionOutbox        *ActionOutboxRepo
	AgentEvents         *AgentEventRepo
	ChannelPolicies     *ChannelPolicyRepo
	ChannelSkips        *ChannelSkipRepo
	ChannelCursors      *ChannelCursorRepo
	ChannelConfigs      *ChannelConfigRepo
	GlobalConfigs       *GlobalConfigRepo
	DailyBriefs         *DailyBriefRepo
}

// NewRepos 在一个 Querier 之上构建仓储（用 WithTx 可得到事务范围的仓储）。
func NewRepos(q Querier) *Repos {
	return &Repos{
		Users:               NewUserRepo(q),
		Persons:             NewPersonRepo(q),
		Facts:               NewFactRepo(q),
		Tasks:               NewTaskRepo(q),
		Reminders:           NewReminderRepo(q),
		Episodes:            NewEpisodeRepo(q),
		Intents:             NewIntentRepo(q),
		Audit:               NewAuditRepo(q),
		Consents:            NewConsentRepo(q),
		Sessions:            NewSessionRepo(q),
		InboxEvents:         NewInboxEventRepo(q),
		InboxSnoozes:        NewInboxSnoozeRepo(q),
		InboxWatches:        NewInboxWatchRepo(q),
		AgentJobs:           NewAgentJobRepo(q),
		AgentNotifications:  NewAgentNotificationRepo(q),
		LongTerm:            NewLongTermRepo(q),
		Threads:             NewThreadRepo(q),
		ThreadLinks:         NewThreadLinkRepo(q),
		ThreadSubscriptions: NewThreadSubscriptionRepo(q),
		Triggers:            NewTriggerRepo(q),
		ActionOutbox:        NewActionOutboxRepo(q),
		AgentEvents:         NewAgentEventRepo(q),
		ChannelPolicies:     NewChannelPolicyRepo(q),
		ChannelSkips:        NewChannelSkipRepo(q),
		ChannelCursors:      NewChannelCursorRepo(q),
		ChannelConfigs:      NewChannelConfigRepo(q),
		GlobalConfigs:       NewGlobalConfigRepo(q),
		DailyBriefs:         NewDailyBriefRepo(q),
	}
}
