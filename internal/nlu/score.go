// Package nlu: extraction scoring, confidence gating and clarify round-trips.
package nlu

import "strings"

// Thresholds mirrors config.Thresholds (kept local to avoid a config dependency).
type Thresholds struct {
	TaskAuto      float64 // >= auto-create (undoable)
	TaskClarify   float64 // >= clarify, else discard
	PersonClarify float64 // person match below this clarifies
	FactConfirmed float64 // fact at/above becomes confirmed
}

// ScoreInput carries the evidence used to adjust the LLM's self-reported confidence.
type ScoreInput struct {
	LLMSelfReported float64
	TimeParsed      bool // deterministic rule parse succeeded
	TimeRequired    bool // this task kind needs a time
	PersonResolved  bool // exactly one person matched
	PersonAmbiguous bool // multiple candidates
	KindConflict    bool // model kind disagrees with deterministic hint
	Evaluative      bool // fact looks like an evaluative label
}

// Score computes the gated confidence per the doc's policy:
// clamp(llm) + bonuses - penalties.
func Score(in ScoreInput) float64 {
	s := in.LLMSelfReported
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	if in.TimeParsed {
		s += 0.05
	}
	if in.PersonResolved {
		s += 0.05
	}
	if in.PersonAmbiguous {
		s -= 0.20
	}
	if in.TimeRequired && !in.TimeParsed {
		s -= 0.15
	}
	if in.KindConflict {
		s -= 0.10
	}
	// Evaluative labels are never auto-accepted regardless of score.
	if in.Evaluative {
		s = 0
	}
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	return s
}

// TaskAction is the gating decision for a task-like extraction.
type TaskAction string

const (
	ActionAutoCreate TaskAction = "auto_create" // >= task_auto, undoable
	ActionClarify    TaskAction = "clarify"     // between thresholds
	ActionDiscard    TaskAction = "discard"     // < task_clarify
)

// GateTask maps a task score to its action.
func GateTask(score float64, th Thresholds) TaskAction {
	switch {
	case score >= th.TaskAuto:
		return ActionAutoCreate
	case score >= th.TaskClarify:
		return ActionClarify
	default:
		return ActionDiscard
	}
}

// evaluativeKeywords mark personality/health/finance judgments that must never
// be auto-written (P5 / "评价性标签 0 条自动生成").
var evaluativeKeywords = []string{
	"性格", "脾气", "人品", "好坏", "小气", "大方", "抠门", "势利",
	"健康", "生病", "有病", "病", "血压", "抑郁", "痴呆", "残疾",
	"穷", "有钱", "没钱", "财务", "负债", "破产",
	"笨", "聪明", "傻", "懒", "固执", "挑剔",
	"不好", "不孝", "不靠谱", "讨厌",
}

// IsEvaluative deterministically flags evaluative (judgment) content.
// The model's own opinion is never trusted for this decision.
func IsEvaluative(factType, valueText string) bool {
	text := factType + " " + valueText
	for _, kw := range evaluativeKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// AllowedFactType enforces "儿童仅事实型": children get factual fact types only.
var childBlockedFactTypes = map[string]bool{
	"personality": true, "health": true, "finance": true,
	"evaluation": true, "ability": true, "appearance": true,
}

// ChildFactAllowed reports whether a fact type may be stored for a child account.
func ChildFactAllowed(factType string) bool {
	return !childBlockedFactTypes[factType]
}
