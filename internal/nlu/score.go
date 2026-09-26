// Package nlu: 抽取评分、置信度门控与澄清往返。
package nlu

import "strings"

// Thresholds 镜像 config.Thresholds（留在本地以避免对 config 的依赖）。
type Thresholds struct {
	TaskAuto      float64 // >= 自动创建（可撤销）
	TaskClarify   float64 // >= 澄清，否则丢弃
	PersonClarify float64 // 人物匹配低于此值则澄清
	FactConfirmed float64 // 事实达到/超过此值即确认
}

// ScoreInput 携带用于调整 LLM 自报置信度的证据。
type ScoreInput struct {
	LLMSelfReported float64
	TimeParsed      bool // 确定性规则解析成功
	TimeRequired    bool // 该任务类型需要时间
	PersonResolved  bool // 恰好匹配到一个人
	PersonAmbiguous bool // 多个候选
	KindConflict    bool // 模型类型与确定性提示不一致
	Evaluative      bool // 事实看起来像评价性标签
}

// Score 按文档策略计算门控后的置信度：
// clamp(llm) + 加分 - 扣分。
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
	// 评价性标签无论分数多高都绝不自动接受。
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

// TaskAction 是类任务抽取的门控决策。
type TaskAction string

const (
	ActionAutoCreate TaskAction = "auto_create" // >= task_auto，可撤销
	ActionClarify    TaskAction = "clarify"     // 介于阈值之间
	ActionDiscard    TaskAction = "discard"     // < task_clarify
)

// GateTask 把任务分数映射到其动作。
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

// evaluativeKeywords 标记性格/健康/财务类判断，这些内容绝不能
// 被自动写入（P5 / "评价性标签 0 条自动生成"）。
var evaluativeKeywords = []string{
	"性格", "脾气", "人品", "好坏", "小气", "大方", "抠门", "势利",
	"健康", "生病", "有病", "病", "血压", "抑郁", "痴呆", "残疾",
	"穷", "有钱", "没钱", "财务", "负债", "破产",
	"笨", "聪明", "傻", "懒", "固执", "挑剔",
	"不好", "不孝", "不靠谱", "讨厌",
}

// IsEvaluative 用确定性规则标记评价性（判断）内容。
// 此决策绝不信任模型自己的意见。
func IsEvaluative(factType, valueText string) bool {
	text := factType + " " + valueText
	for _, kw := range evaluativeKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// AllowedFactType 落实"儿童仅事实型"：儿童只能存事实类事实类型。
var childBlockedFactTypes = map[string]bool{
	"personality": true, "health": true, "finance": true,
	"evaluation": true, "ability": true, "appearance": true,
}

// ChildFactAllowed 报告某事实类型是否可为儿童账户存储。
func ChildFactAllowed(factType string) bool {
	return !childBlockedFactTypes[factType]
}
