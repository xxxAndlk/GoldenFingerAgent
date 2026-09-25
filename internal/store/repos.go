package store

// Repos aggregates all repositories over one Querier (pool or tx).
type Repos struct {
	Users     *UserRepo
	Persons   *PersonRepo
	Facts     *FactRepo
	Tasks     *TaskRepo
	Reminders *ReminderRepo
	Episodes  *EpisodeRepo
	Audit     *AuditRepo
	Consents  *ConsentRepo
	Sessions  *SessionRepo
}

// NewRepos builds repos over a Querier (use WithTx to get transaction-scoped repos).
func NewRepos(q Querier) *Repos {
	return &Repos{
		Users:     NewUserRepo(q),
		Persons:   NewPersonRepo(q),
		Facts:     NewFactRepo(q),
		Tasks:     NewTaskRepo(q),
		Reminders: NewReminderRepo(q),
		Episodes:  NewEpisodeRepo(q),
		Audit:     NewAuditRepo(q),
		Consents:  NewConsentRepo(q),
		Sessions:  NewSessionRepo(q),
	}
}
