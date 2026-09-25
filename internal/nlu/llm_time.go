package nlu

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu/timecn"
)

const timeFallbackPrompt = `把下面的中文时间表达换算成绝对时间。当前时间: %s。
输出 JSON，不要输出其他内容:
{"abs_time":"RFC3339","recurrence":"","confidence":0.0}
- recurrence 留空，或 "daily@HH:MM"，或 "weekly@WnTHH:MM"（W1=周一..W7=周日），或 "monthly@DT HH:MM"。
- abs_time 必须是 RFC3339 格式且在未来。
- confidence 取 0~1，表示你对换算结果的把握。`

var reRecurrence = regexp.MustCompile(`^(daily@\d{2}:\d{2}|weekly@W[1-7]T\d{2}:\d{2}|monthly@\d{1,2}T\d{2}:\d{2})$`)

// llmTimeFallback asks the LLM to normalize a time phrase the rules missed,
// then HARD-VALIDATES the answer. Invalid answers are rejected (ok=false).
func llmTimeFallback(ctx context.Context, client llm.Client, model, rawExpr string, now time.Time, loc *time.Location) (timecn.TimeResult, bool) {
	prompt := fmt.Sprintf(timeFallbackPrompt, now.In(loc).Format(time.RFC3339)) + "\n时间表达: " + rawExpr
	resp, err := client.Chat(ctx, llm.ChatRequest{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: prompt},
		},
		JSONMode:    true,
		Temperature: 0,
		MaxTokens:   256,
	})
	if err != nil {
		return timecn.TimeResult{}, false
	}

	var out struct {
		AbsTime    string  `json:"abs_time"`
		Recurrence string  `json:"recurrence"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(resp.Message.Content), &out); err != nil {
		return timecn.TimeResult{}, false
	}

	// Hard validation — the model is never trusted blindly.
	abs, err := time.Parse(time.RFC3339, strings.TrimSpace(out.AbsTime))
	if err != nil {
		return timecn.TimeResult{}, false
	}
	abs = abs.In(loc)
	if !abs.After(now) {
		return timecn.TimeResult{}, false
	}
	rec := strings.TrimSpace(out.Recurrence)
	if rec != "" && !reRecurrence.MatchString(rec) {
		return timecn.TimeResult{}, false
	}
	return timecn.TimeResult{
		Abs:        abs,
		Recurrence: rec,
		Raw:        rawExpr,
		Method:     "llm",
	}, true
}
