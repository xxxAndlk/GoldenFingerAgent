package store

// Repos 在一个 Querier（连接池或事务）之上聚合所有仓储。
type Repos struct {
	Users     *UserRepo
	Persons   *PersonRepo
	Facts     *FactRepo
	Tasks     *TaskRepo
	Reminders *ReminderRepo
	Episodes  *EpisodeRepo
	Intents   *IntentRepo
	Audit     *AuditRepo
	Consents  *ConsentRepo
	Sessions  *SessionRepo
}

// NewRepos 在一个 Querier 之上构建仓储（用 WithTx 可得到事务范围的仓储）。
func NewRepos(q Querier) *Repos {
	return &Repos{
		Users:     NewUserRepo(q),
		Persons:   NewPersonRepo(q),
		Facts:     NewFactRepo(q),
		Tasks:     NewTaskRepo(q),
		Reminders: NewReminderRepo(q),
		Episodes:  NewEpisodeRepo(q),
		Intents:   NewIntentRepo(q),
		Audit:     NewAuditRepo(q),
		Consents:  NewConsentRepo(q),
		Sessions:  NewSessionRepo(q),
	}
}
