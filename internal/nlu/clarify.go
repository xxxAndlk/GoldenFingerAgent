package nlu

import (
	"encoding/json"
	"strings"
	"time"
)

// PendingActionType enumerates clarify round-trip kinds.
const (
	PendingTaskCreate     = "task_create"
	PendingPersonDisambig = "person_disambig"
	PendingFactConfirm    = "fact_confirm"
	PendingTimeMissing    = "time_missing"
)

// Option is one selectable clarify answer shown to the user.
type Option struct {
	Label string `json:"label"` // Chinese user-facing text
	Ref   string `json:"ref"`   // e.g. "person:<uuid>" | "yes" | "no" | "none"
}

// PendingAction is the frozen hypothesis awaiting the user's next message.
// It lives in chat_session.state_jsonb so it survives restarts.
type PendingAction struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"` // frozen hypothesis (raw_text, kind, candidates…)
	Options   []Option        `json:"options,omitempty"`
	Question  string          `json:"question"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// DefaultPendingTTL is how long a clarify question stays answerable.
const DefaultPendingTTL = 10 * time.Minute

// NewPending builds a pending action with the default TTL.
func NewPending(typ string, payload json.RawMessage, question string, opts []Option, now time.Time) *PendingAction {
	return &PendingAction{
		Type:      typ,
		Payload:   payload,
		Options:   opts,
		Question:  question,
		CreatedAt: now,
		ExpiresAt: now.Add(DefaultPendingTTL),
	}
}

// Expired reports whether the pending action is too old to resolve.
func (p *PendingAction) Expired(now time.Time) bool {
	return p == nil || now.After(p.ExpiresAt)
}

// MatchOutcome is the result of matching the user's reply against a pending action.
type MatchOutcome int

const (
	MatchNone   MatchOutcome = iota // message is not an answer → abandon/ignore
	MatchOption                     // picked one of Options (or 都不是)
	MatchAffirm                     // "对/是的/嗯" style confirmation
	MatchDeny                       // "不对/不是/取消" style rejection
)

// affirm/deny word sets for yes/no clarifications.
var (
	affirmWords = []string{"对", "对的", "是的", "是", "嗯", "好", "好的", "可以", "确认", "没错", "就这样", "ok", "OK", "行"}
	denyWords   = []string{"不对", "不是", "不用", "不要", "算了", "取消", "不是的", "没对", "错", "不"}
	noneWords   = []string{"都不是", "没有", "都不对", "其他", "都不是的"}
)

// Match classifies the user's reply for a pending action.
// Option matching wins over bare affirm/deny (a listed option is more specific).
func Match(p *PendingAction, userText string) (MatchOutcome, *Option) {
	if p == nil {
		return MatchNone, nil
	}
	text := strings.TrimSpace(userText)
	if text == "" {
		return MatchNone, nil
	}

	// Option selection: exact label, contains-label, or numeric index ("1", "二").
	for i := range p.Options {
		opt := &p.Options[i]
		if text == opt.Label || strings.Contains(text, opt.Label) {
			return MatchOption, opt
		}
	}
	if idx, ok := parseIndex(text); ok && idx >= 1 && idx <= len(p.Options) {
		return MatchOption, &p.Options[idx-1]
	}
	for _, w := range noneWords {
		if text == w {
			return MatchOption, &Option{Label: "都不是", Ref: "none"}
		}
	}

	// Affirm / deny fallback for yes-no questions.
	for _, w := range denyWords {
		if text == w {
			return MatchDeny, nil
		}
	}
	for _, w := range affirmWords {
		if text == w {
			return MatchAffirm, nil
		}
	}
	return MatchNone, nil
}

// parseIndex recognizes "1", "2", "一", "二"… as option indices.
func parseIndex(text string) (int, bool) {
	switch text {
	case "1", "一":
		return 1, true
	case "2", "二":
		return 2, true
	case "3", "三":
		return 3, true
	case "4", "四":
		return 4, true
	case "5", "五":
		return 5, true
	}
	return 0, false
}

// ---- frozen payloads ----

// TaskPayload is the frozen create_task hypothesis.
type TaskPayload struct {
	RawText       string  `json:"raw_text"`
	Kind          string  `json:"kind"` // intent|fact|alarm|note
	Title         string  `json:"title"`
	TimeExprRaw   string  `json:"time_expr_raw"`
	PersonName    string  `json:"person_name,omitempty"`
	PersonID      string  `json:"person_id,omitempty"`
	EventTemplate string  `json:"event_template,omitempty"`
	Confidence    float64 `json:"confidence"`
}

// PersonCandidate is one disambiguation choice.
type PersonCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PersonDisambigPayload freezes the action that needs a person choice.
type PersonDisambigPayload struct {
	NextAction string            `json:"next_action"` // "task_create" | "save_fact"
	Task       *TaskPayload      `json:"task,omitempty"`
	Fact       *FactPayload      `json:"fact,omitempty"`
	Candidates []PersonCandidate `json:"candidates"`
}

// FactPayload is the frozen save_fact hypothesis.
type FactPayload struct {
	PersonName string  `json:"person_name"`
	PersonID   string  `json:"person_id,omitempty"`
	FactType   string  `json:"fact_type"`
	ValueText  string  `json:"value_text"`
	Evaluative bool    `json:"evaluative"`
	Confidence float64 `json:"confidence"`
}

// TimeMissingPayload freezes a task awaiting a time expression.
type TimeMissingPayload struct {
	Task TaskPayload `json:"task"`
}
