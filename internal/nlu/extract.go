package nlu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu/timecn"
)

// Extraction is the structured output of the fallback extractor.
type Extraction struct {
	Persons   []ExtractedPerson `json:"persons"`
	Tasks     []ExtractedTask   `json:"tasks"`
	Note      *ExtractedNote    `json:"note"`
	SelfScore float64           `json:"self_reported_confidence"`
}

type ExtractedPerson struct {
	Name     string          `json:"name"`
	Aliases  []string        `json:"alias"`
	Relation string          `json:"relation"`
	Facts    []ExtractedFact `json:"facts"`
}

type ExtractedFact struct {
	FactType   string `json:"fact_type"`
	ValueText  string `json:"value_text"`
	Evaluative bool   `json:"evaluative"` // model's guess; re-checked deterministically
}

type ExtractedTask struct {
	Kind         string `json:"kind"` // intent|fact|alarm|note
	Title        string `json:"title"`
	PersonName   string `json:"person_name"`
	TimeExprRaw  string `json:"time_expr_raw"`
	DeadlineExpr string `json:"deadline_expr_raw"`
}

type ExtractedNote struct {
	Text string `json:"text"`
	Tags string `json:"tags"`
}

const extractSystemPrompt = `你是信息抽取器。从用户话语中抽取人物、待办、备忘，输出 JSON，不要输出其他内容。
JSON 结构:
{"persons":[{"name":"","alias":[],"relation":"","facts":[{"fact_type":"","value_text":"","evaluative":false}]}],
 "tasks":[{"kind":"intent|fact|alarm|note","title":"","person_name":"","time_expr_raw":"","deadline_expr_raw":""}],
 "note":{"text":"","tags":""},
 "self_reported_confidence":0.0}
规则:
- kind: intent=打算做但没做, fact=已经发生的事, alarm=闹钟/倒计时, note=随手记。
- time_expr_raw 保留用户原话（如"后天""明早7点"），不要自己换算。
- evaluative=true 表示这是对人的评价（性格/健康/财务等），事实型为 false。
- self_reported_confidence 取 0~1。`

// Extract runs the LLM structured-output fallback for ambiguous utterances.
func Extract(ctx context.Context, client llm.Client, model, userText string) (*Extraction, error) {
	resp, err := client.Chat(ctx, llm.ChatRequest{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: extractSystemPrompt},
			{Role: llm.RoleUser, Content: userText},
		},
		JSONMode:    true,
		Temperature: 0.1,
		MaxTokens:   1024,
	})
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	var out Extraction
	if err := json.Unmarshal([]byte(resp.Message.Content), &out); err != nil {
		return nil, fmt.Errorf("extract: bad JSON: %w", err)
	}
	return &out, nil
}

// LLMTimeFallback asks the LLM to normalize a time phrase the rules missed.
// The answer is HARD-VALIDATED before use (see llm_time.go).
func LLMTimeFallback(ctx context.Context, client llm.Client, model, rawExpr string, now time.Time, loc *time.Location) (timecn.TimeResult, bool) {
	return llmTimeFallback(ctx, client, model, rawExpr, now, loc)
}
