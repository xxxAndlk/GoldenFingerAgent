// Package openai is the OpenAI Responses-protocol chat client plus an
// OpenAI-compatible embeddings client. Any gateway speaking POST /responses
// (DeepSeek, OpenAI, one-api/new-api relays) works by swapping base_url+model.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/llm"
)

type Client struct {
	BaseURL    string
	APIKey     string
	Model      string
	EmbedModel string
	HTTP       *http.Client
}

func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// ---- Responses API (/responses) ----

type contentPart struct {
	Type string `json:"type"` // "input_text" | "output_text"
	Text string `json:"text"`
}

type inputItem struct {
	Type      string        `json:"type"` // "message" | "function_call" | "function_call_output"
	Role      string        `json:"role,omitempty"`
	Content   []contentPart `json:"content,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Output    string        `json:"output,omitempty"`
}

type respTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responsesRequest struct {
	Model           string      `json:"model"`
	Instructions    string      `json:"instructions,omitempty"`
	Input           []inputItem `json:"input"`
	Tools           []respTool  `json:"tools,omitempty"`
	Temperature     float64     `json:"temperature,omitempty"`
	MaxOutputTokens int         `json:"max_output_tokens,omitempty"`
	Stream          bool        `json:"stream,omitempty"`
	Text            *textSpec   `json:"text,omitempty"`
}

type textSpec struct {
	Format struct {
		Type string `json:"type"` // "text" | "json_object"
	} `json:"format"`
}

type outputItem struct {
	Type    string `json:"type"` // "message" | "function_call" | ...
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesResponse struct {
	Status string       `json:"status"` // "completed" | "incomplete" | "failed" | ...
	Output []outputItem `json:"output"`
	Usage  *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamEvent is the superset of SSE event payloads we care about.
type streamEvent struct {
	Type     string             `json:"type"`
	Delta    string             `json:"delta"`
	Response *responsesResponse `json:"response"`
}

func (c *Client) buildRequest(req llm.ChatRequest, stream bool) *responsesRequest {
	out := &responsesRequest{Model: req.Model, Stream: stream}
	if out.Model == "" {
		out.Model = c.Model
	}
	if req.Temperature > 0 {
		out.Temperature = req.Temperature
	}
	if req.MaxTokens > 0 {
		out.MaxOutputTokens = req.MaxTokens
	}
	var instructions []string
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleSystem:
			instructions = append(instructions, m.Content)
		case llm.RoleUser:
			out.Input = append(out.Input, inputItem{
				Type:    "message",
				Role:    "user",
				Content: []contentPart{{Type: "input_text", Text: m.Content}},
			})
		case llm.RoleAssistant:
			if m.Content != "" {
				out.Input = append(out.Input, inputItem{
					Type:    "message",
					Role:    "assistant",
					Content: []contentPart{{Type: "output_text", Text: m.Content}},
				})
			}
			for _, tc := range m.ToolCalls {
				out.Input = append(out.Input, inputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				})
			}
		case llm.RoleTool:
			out.Input = append(out.Input, inputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})
		}
	}
	out.Instructions = strings.Join(instructions, "\n")
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, respTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	if req.JSONMode {
		ts := &textSpec{}
		ts.Format.Type = "json_object"
		out.Text = ts
	}
	return out
}

func convertResponse(rr *responsesResponse) *llm.ChatResponse {
	msg := llm.Message{Role: llm.RoleAssistant}
	var content strings.Builder
	for _, item := range rr.Output {
		switch item.Type {
		case "message":
			for _, p := range item.Content {
				if p.Type == "output_text" {
					content.WriteString(p.Text)
				}
			}
		case "function_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
				ID:        id,
				Name:      item.Name,
				Arguments: json.RawMessage(item.Arguments),
			})
		}
	}
	msg.Content = content.String()
	finish := rr.Status
	if finish == "completed" {
		finish = "stop"
	}
	resp := &llm.ChatResponse{Message: msg, FinishReason: finish}
	if rr.Usage != nil {
		resp.Usage = llm.Usage{PromptTokens: rr.Usage.InputTokens, CompletionTokens: rr.Usage.OutputTokens}
	}
	return resp
}

func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	raw, err := c.doJSON(ctx, "/responses", c.buildRequest(req, false))
	if err != nil {
		return nil, err
	}
	var rr responsesResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("parse responses reply: %w", err)
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("llm error: %s", rr.Error.Message)
	}
	if len(rr.Output) == 0 {
		return nil, fmt.Errorf("llm: empty output (status %q)", rr.Status)
	}
	return convertResponse(&rr), nil
}

// Stream emits SSE text deltas and returns the assembled final message.
func (c *Client) Stream(ctx context.Context, req llm.ChatRequest, onDelta func(string)) (*llm.ChatResponse, error) {
	payload, err := json.Marshal(c.buildRequest(req, true))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm stream: HTTP %d: %s", resp.StatusCode, b)
	}

	var content strings.Builder
	var final *responsesResponse
	var streamErr error
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			content.WriteString(ev.Delta)
			if onDelta != nil {
				onDelta(ev.Delta)
			}
		case "response.completed", "response.incomplete":
			if ev.Response != nil {
				final = ev.Response
			}
		case "response.failed":
			if ev.Response != nil && ev.Response.Error != nil {
				streamErr = fmt.Errorf("llm stream failed: %s", ev.Response.Error.Message)
			} else {
				streamErr = fmt.Errorf("llm stream failed")
			}
		case "error":
			streamErr = fmt.Errorf("llm stream error")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read: %w", err)
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if final != nil {
		return convertResponse(final), nil
	}
	// Gateway ended without a completed event: return what we accumulated.
	return &llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: content.String()},
		FinishReason: "stop",
	}, nil
}

// ---- embeddings (OpenAI-compatible /embeddings) ----

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed implements llm.Embedder.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	model := c.EmbedModel
	if model == "" {
		model = c.Model
	}
	raw, err := c.doJSON(ctx, "/embeddings", embedRequest{Model: model, Input: texts})
	if err != nil {
		return nil, err
	}
	var er embedResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("parse embed response: %w", err)
	}
	if er.Error != nil {
		return nil, fmt.Errorf("embed error: %s", er.Error.Message)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embed: got %d vectors for %d texts", len(er.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for i, d := range er.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// ---- plumbing ----

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}

func (c *Client) doJSON(ctx context.Context, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, raw)
	}
	return raw, nil
}
