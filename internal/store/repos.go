package store

// Repos 在一个 Querier（连接池或事务）之上聚合所有仓储。
type Repos struct {
	// DB 仅在使用连接池构造时非空；事务内仓储为 nil，防止误在事务外开新事务。
	DB                  *DB
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
	WorkTasks           *WorkTaskRepo
	WorkTaskLive        *WorkTaskLiveStateRepo
	WorkTaskThreads     *WorkTaskThreadRepo
	WorkTaskInbox       *WorkTaskInboxRepo
	Artifacts           *ArtifactRepo
	ArtifactVersions    *ArtifactVersionRepo
	Checkpoints         *CheckpointRepo
	Grants              *ActionGrantRepo
	ToolInvocations     *ToolInvocationRepo
	PersonalPolicies    *PersonalPolicyRepo
	Devices             *DeviceRepo
	ConnectorStatuses   *ConnectorStatusRepo
	CloudDevices        *CloudDeviceRepo
	CloudEpisodes       *CloudEpisodeMetaRepo
	CloudDeliveries     *CloudDeliveryRepo
	MemoryOutbox        *MemoryOutboxRepo
	DeliveryOutbox      *DeliveryOutboxRepo
}

// NewRepos 在一个 Querier 之上构建仓储（用 WithTx 可得到事务范围的仓储）。
func NewRepos(q Querier) *Repos {
	var db *DB
	if value, ok := q.(*DB); ok {
		db = value
	}
	return &Repos{
		DB:                  db,
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
		WorkTasks:           NewWorkTaskRepo(q),
		WorkTaskLive:        NewWorkTaskLiveStateRepo(q),
		WorkTaskThreads:     NewWorkTaskThreadRepo(q),
		WorkTaskInbox:       NewWorkTaskInboxRepo(q),
		Artifacts:           NewArtifactRepo(q),
		ArtifactVersions:    NewArtifactVersionRepo(q),
		Checkpoints:         NewCheckpointRepo(q),
		Grants:              NewActionGrantRepo(q),
		ToolInvocations:     NewToolInvocationRepo(q),
		PersonalPolicies:    NewPersonalPolicyRepo(q),
		Devices:             NewDeviceRepo(q),
		ConnectorStatuses:   NewConnectorStatusRepo(q),
		CloudDevices:        NewCloudDeviceRepo(q),
		CloudEpisodes:       NewCloudEpisodeMetaRepo(q),
		CloudDeliveries:     NewCloudDeliveryRepo(q),
		MemoryOutbox:        NewMemoryOutboxRepo(q),
		DeliveryOutbox:      NewDeliveryOutboxRepo(q),
	}
}
