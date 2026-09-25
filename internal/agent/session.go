// Package agent is the pi-style tool-calling loop + session state
// (pi's packages/agent analog). Product rules live in domain packages;
// this layer is a dumb cycle: append → LLM → tools → repeat.
package agent

import (
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu"
)

// Card is a UI action card (task/reminder with 完成/推迟/取消 buttons).
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

// Session carries per-conversation state (pi's agent.ts analog).
type Session struct {
	ID       string
	UserID   string
	Messages []llm.Message
	Pending  *nlu.PendingAction `json:"-"`
	Trace    []string           // debug tool trace
}

// Append adds one message to the transcript.
func (s *Session) Append(m llm.Message) { s.Messages = append(s.Messages, m) }

// TurnResult is the user-facing outcome of one conversation turn.
type TurnResult struct {
	Reply   string             `json:"reply"`
	Pending *nlu.PendingAction `json:"pending,omitempty"`
	Cards   []Card             `json:"cards,omitempty"`
	Trace   []string           `json:"trace,omitempty"`
}

// Clock abstracts time for deterministic tests.
type Clock interface{ Now() time.Time }

// SystemClock is the production clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// FixedClock returns a constant time (tests).
type FixedClock struct{ T time.Time }

func (f FixedClock) Now() time.Time { return f.T }
