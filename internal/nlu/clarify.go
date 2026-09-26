package nlu

import (
	"encoding/json"
	"strings"
	"time"
)

// PendingActionType 枚举澄清往返的类型。
const (
	PendingTaskCreate     = "task_create"
	PendingPersonDisambig = "person_disambig"
	PendingFactConfirm    = "fact_confirm"
	PendingTimeMissing    = "time_missing"
)

// Option 是展示给用户的可选澄清答案。
type Option struct {
	Label string `json:"label"` // 面向用户的中文文本
	Ref   string `json:"ref"`   // 例如："person:<uuid>" | "yes" | "no" | "none"
}

// PendingAction 是等待用户下一条消息的冻结假设。
// 它存放在 chat_session.state_jsonb 中，因此重启后依然存在。
type PendingAction struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"` // 冻结假设（raw_text、kind、candidates…）
	Options   []Option        `json:"options,omitempty"`
	Question  string          `json:"question"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// DefaultPendingTTL 是澄清问题保持可回答的时长。
const DefaultPendingTTL = 10 * time.Minute

// NewPending 使用默认 TTL 构建一个待定动作。
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

// Expired 报告该待定动作是否已过期、无法再解析。
func (p *PendingAction) Expired(now time.Time) bool {
	return p == nil || now.After(p.ExpiresAt)
}

// MatchOutcome 是将用户回复与待定动作匹配的结果。
type MatchOutcome int

const (
	MatchNone   MatchOutcome = iota // 消息不是回答 → 放弃/忽略
	MatchOption                     // 选中了某个选项（或「都不是」）
	MatchAffirm                     // 「对/是的/嗯」式确认
	MatchDeny                       // 「不对/不是/取消」式拒绝
)

// 用于是/否澄清的肯定/否定词集。
var (
	affirmWords = []string{"对", "对的", "是的", "是", "嗯", "好", "好的", "可以", "确认", "没错", "就这样", "ok", "OK", "行"}
	denyWords   = []string{"不对", "不是", "不用", "不要", "算了", "取消", "不是的", "没对", "错", "不"}
	noneWords   = []string{"都不是", "没有", "都不对", "其他", "都不是的"}
)

// Match 对待定动作的用户回复进行分类。
// 选项匹配优先于单纯的肯定/否定（列出的选项更具体）。
func Match(p *PendingAction, userText string) (MatchOutcome, *Option) {
	if p == nil {
		return MatchNone, nil
	}
	text := strings.TrimSpace(userText)
	if text == "" {
		return MatchNone, nil
	}

	// 选项选择：精确标签、包含标签或数字序号（"1"、"二"）。
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

	// 是非题的肯定/否定兜底。
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

// parseIndex 识别 "1"、"2"、"一"、"二"… 作为选项序号。
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

// ---- 冻结载荷 ----

// TaskPayload 是冻结的 create_task 假设。
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

// PersonCandidate 是消歧选项之一。
type PersonCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PersonDisambigPayload 冻结需要用户选择人物的动作。
type PersonDisambigPayload struct {
	NextAction string            `json:"next_action"` // "task_create" | "save_fact"
	Task       *TaskPayload      `json:"task,omitempty"`
	Fact       *FactPayload      `json:"fact,omitempty"`
	Candidates []PersonCandidate `json:"candidates"`
}

// FactPayload 是冻结的 save_fact 假设。
type FactPayload struct {
	PersonName string  `json:"person_name"`
	PersonID   string  `json:"person_id,omitempty"`
	FactType   string  `json:"fact_type"`
	ValueText  string  `json:"value_text"`
	Evaluative bool    `json:"evaluative"`
	Confidence float64 `json:"confidence"`
}

// TimeMissingPayload 冻结等待时间表达式的任务。
type TimeMissingPayload struct {
	Task TaskPayload `json:"task"`
}
