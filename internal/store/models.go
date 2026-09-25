package store

import (
	"encoding/json"
	"time"
)

// User types.
const (
	UserElder   = "elder"
	UserGeneral = "general"
	UserChild   = "child"
)

type User struct {
	ID         string          `json:"id"`
	UserType   string          `json:"user_type"`
	Name       string          `json:"name"`
	TZ         string          `json:"tz"`
	GuardianID *string         `json:"guardian_id,omitempty"`
	NotifPrefs json.RawMessage `json:"notif_prefs,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	DeletedAt  *time.Time      `json:"deleted_at,omitempty"`
}

type Person struct {
	ID            string     `json:"id"`
	OwnerUserID   string     `json:"owner_user_id"`
	CanonicalName string     `json:"canonical_name"`
	Notes         string     `json:"notes"`
	CreatedAt     time.Time  `json:"created_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
}

type Alias struct {
	ID       string `json:"id"`
	PersonID string `json:"person_id"`
	Alias    string `json:"alias"`
}

// Fact statuses.
const (
	FactConfirmed = "confirmed"
	FactInferred  = "inferred"
)

type Fact struct {
	ID          string     `json:"id"`
	PersonID    string     `json:"person_id"`
	FactType    string     `json:"fact_type"`
	ValueText   string     `json:"value_text"`
	Confidence  float64    `json:"confidence"`
	Status      string     `json:"status"`
	SourceMsgID *string    `json:"source_msg_id,omitempty"`
	ValidFrom   time.Time  `json:"valid_from"`
	ValidTo     *time.Time `json:"valid_to,omitempty"`
	Embedding   []float32  `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// Task kinds.
const (
	KindIntent = "intent"
	KindFact   = "fact"
	KindAlarm  = "alarm"
	KindNote   = "note"
)

// Task statuses (state machine nodes).
const (
	TaskDraft          = "draft"
	TaskPendingConfirm = "pending_confirm"
	TaskScheduled      = "scheduled"
	TaskNotified       = "notified"
	TaskDone           = "done"
	TaskSnoozed        = "snoozed"
	TaskCancelled      = "cancelled"
	TaskExpired        = "expired"
)

type Task struct {
	ID             string          `json:"id"`
	OwnerUserID    string          `json:"owner_user_id"`
	Kind           string          `json:"kind"`
	Schema         json.RawMessage `json:"schema,omitempty"`
	TimeExprRaw    string          `json:"time_expr_raw"`
	AbsTime        *time.Time      `json:"abs_time,omitempty"`
	Deadline       *time.Time      `json:"deadline,omitempty"`
	Status         string          `json:"status"`
	Confidence     float64         `json:"confidence"`
	SourceMsgID    *string         `json:"source_msg_id,omitempty"`
	LinkedPersonID *string         `json:"linked_person_id,omitempty"`
	EventTemplate  string          `json:"event_template"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	DeletedAt      *time.Time      `json:"deleted_at,omitempty"`
}

// Reminder states.
const (
	ReminderPending = "pending"
	ReminderSent    = "sent"
	ReminderAcked   = "acked"
	ReminderFailed  = "failed"
)

type Reminder struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	FireAt    time.Time `json:"fire_at"`
	Channel   string    `json:"channel"`
	Level     int       `json:"level"`
	State     string    `json:"state"`
	DedupeKey string    `json:"dedupe_key"`
	CreatedAt time.Time `json:"created_at"`
}

type Episode struct {
	ID          string     `json:"id"`
	OwnerUserID string     `json:"owner_user_id"`
	Summary     string     `json:"summary"`
	RawRef      string     `json:"raw_ref"`
	TimeStart   *time.Time `json:"time_start,omitempty"`
	TimeEnd     *time.Time `json:"time_end,omitempty"`
	Embedding   []float32  `json:"-"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

type AuditEntry struct {
	ID          int64           `json:"id"`
	OwnerUserID *string         `json:"owner_user_id,omitempty"`
	Actor       string          `json:"actor"`
	Action      string          `json:"action"`
	Target      string          `json:"target"`
	Detail      json.RawMessage `json:"detail,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

type Consent struct {
	ID                  string     `json:"id"`
	SubjectUserID       string     `json:"subject_user_id"`
	Scope               string     `json:"scope"`
	GrantedByGuardianID *string    `json:"granted_by_guardian_id,omitempty"`
	GrantedAt           time.Time  `json:"granted_at"`
	RevokedAt           *time.Time `json:"revised_at,omitempty"`
}

type ChatSession struct {
	ID          string          `json:"id"`
	OwnerUserID string          `json:"owner_user_id"`
	State       json.RawMessage `json:"state,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type ChatMessage struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Scored* wrap similarity query results.
type ScoredFact struct {
	Fact
	Score float64 `json:"score"`
}

type ScoredEpisode struct {
	Episode
	Score float64 `json:"score"`
}
