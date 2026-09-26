// Package agent 是 pi 风格的工具调用循环 + 会话状态
// （对应 pi 的 packages/agent）。产品规则位于领域包中；
// 本层是一个朴素循环：追加 → LLM → 工具 → 重复。
package agent

import (
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu"
)

// Card 是一张 UI 操作卡（带 完成/推迟/取消 按钮的任务/提醒）。
type Card struct {
	Type    string       `json:"type"` // "task" | "reminder" | "person" | "note"
	Title   string       `json:"title"`
	Body    string       `json:"body,omitempty"`
	RefID   string       `json:"ref_id,omitempty"`
	Actions []CardAction `json:"actions,omitempty"`
}

type CardAction struct {
	Label string `json:"label"` // 完成 / 推迟 / 取消
	Verb  string `json:"verb"`  // done / snooze / cancel
}

// Session 承载每次对话的状态（对应 pi 的 agent.ts）。
type Session struct {
	ID       string
	UserID   string
	Messages []llm.Message
	Pending  *nlu.PendingAction `json:"-"`
	Trace    []string           // 调试用工具轨迹
}

// Append 向对话记录追加一条消息。
func (s *Session) Append(m llm.Message) { s.Messages = append(s.Messages, m) }

// TurnResult 是一轮对话面向用户的输出结果。
type TurnResult struct {
	Reply   string             `json:"reply"`
	Pending *nlu.PendingAction `json:"pending,omitempty"`
	Cards   []Card             `json:"cards,omitempty"`
	Trace   []string           `json:"trace,omitempty"`
}

// Clock 抽象时间，便于确定性测试。
type Clock interface{ Now() time.Time }

// SystemClock 是生产环境时钟。
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// FixedClock 返回恒定时间（测试用）。
type FixedClock struct{ T time.Time }

func (f FixedClock) Now() time.Time { return f.T }
