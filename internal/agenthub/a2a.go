package agenthub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type agentCard struct {
	Name                string `json:"name"`
	Description         string `json:"description"`
	URL                 string `json:"url"`
	SupportedInterfaces []struct {
		URL             string `json:"url"`
		ProtocolBinding string `json:"protocolBinding"`
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"supportedInterfaces"`
	Skills []struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	} `json:"skills"`
}

func (m *Manager) discoverA2A(parent context.Context, endpoint Endpoint) Agent {
	agent := Agent{ID: endpoint.ID, Name: endpoint.Name, Kind: "a2a", Installed: true, Endpoint: endpoint.URL, Description: "A2A Agent", Capabilities: []string{"scan"}}
	if endpoint.ID == "bma" {
		agent.Kind = "bma"
		agent.Name = "BMA"
		agent.Capabilities = append(agent.Capabilities, "project")
	}
	ctx, cancel := context.WithTimeout(parent, 6*time.Second)
	defer cancel()
	cardURL := endpoint.URL
	if !strings.HasSuffix(cardURL, "/.well-known/agent-card.json") && !strings.HasSuffix(cardURL, "/.well-known/agent.json") {
		cardURL += "/.well-known/agent-card.json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		agent.Detail = err.Error()
		return agent
	}
	if endpoint.Token != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.Token)
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		agent.Detail = "Agent Card 不可用：" + err.Error()
		return agent
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		agent.Detail = fmt.Sprintf("Agent Card HTTP %d", resp.StatusCode)
		return agent
	}
	var card agentCard
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&card); err != nil {
		agent.Detail = "Agent Card 解析失败：" + err.Error()
		return agent
	}
	agent.Running = true
	if card.Name != "" && endpoint.ID != "bma" {
		agent.Name = card.Name
	}
	if card.Description != "" {
		agent.Description = card.Description
	}
	agent.Endpoint = strings.TrimRight(card.URL, "/")
	for _, item := range card.SupportedInterfaces {
		if item.URL != "" && (item.ProtocolBinding == "" || strings.EqualFold(item.ProtocolBinding, "JSONRPC")) {
			agent.Endpoint = strings.TrimRight(item.URL, "/")
			break
		}
	}
	if agent.Endpoint == "" {
		agent.Endpoint = endpoint.URL
	}
	for _, skill := range card.Skills {
		name := skill.Name
		if name == "" {
			name = skill.ID
		}
		if name != "" {
			agent.Capabilities = append(agent.Capabilities, name)
		}
	}
	agent.Capabilities = append(agent.Capabilities, "delegate")
	return agent
}

type a2aPart struct {
	Kind string `json:"kind,omitempty"`
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type a2aMessage struct {
	Kind      string    `json:"kind,omitempty"`
	Role      string    `json:"role"`
	Parts     []a2aPart `json:"parts"`
	MessageID string    `json:"messageId"`
	TaskID    string    `json:"taskId,omitempty"`
	ContextID string    `json:"contextId,omitempty"`
}

type a2aResult struct {
	Kind   string    `json:"kind"`
	ID     string    `json:"id"`
	Parts  []a2aPart `json:"parts"`
	Status struct {
		State   string     `json:"state"`
		Message a2aMessage `json:"message"`
	} `json:"status"`
	Artifacts []struct {
		Parts []a2aPart `json:"parts"`
	} `json:"artifacts"`
	History []a2aMessage `json:"history"`
}

func (m *Manager) invokeA2A(ctx context.Context, agent Agent, task string) (Invocation, error) {
	endpoints, err := m.endpoints()
	if err != nil {
		return Invocation{}, err
	}
	var token string
	for _, endpoint := range endpoints {
		if endpoint.ID == agent.ID {
			token = endpoint.Token
			break
		}
	}
	payload := map[string]any{
		"jsonrpc": "2.0", "id": uuid.NewString(), "method": "message/send",
		"params": map[string]any{
			"message":       a2aMessage{Role: "user", MessageID: uuid.NewString(), Parts: []a2aPart{{Kind: "text", Text: task}}},
			"configuration": map[string]any{"blocking": true, "acceptedOutputModes": []string{"text/plain"}},
		},
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agent.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return Invocation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", "1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return Invocation{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return Invocation{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Invocation{}, fmt.Errorf("A2A HTTP %d：%s", resp.StatusCode, truncateText(string(body), 1000))
	}
	var rpc struct {
		Result a2aResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return Invocation{}, fmt.Errorf("A2A 响应解析失败：%w", err)
	}
	if rpc.Error != nil {
		return Invocation{}, fmt.Errorf("A2A %d：%s", rpc.Error.Code, rpc.Error.Message)
	}
	result := rpc.Result
	output := partsText(result.Parts)
	if output == "" {
		output = partsText(result.Status.Message.Parts)
	}
	if output == "" {
		for _, artifact := range result.Artifacts {
			output += partsText(artifact.Parts)
		}
	}
	if output == "" && len(result.History) > 0 {
		output = partsText(result.History[len(result.History)-1].Parts)
	}
	if output == "" {
		return Invocation{}, errors.New("A2A 已响应，但没有文本结果")
	}
	return Invocation{AgentID: agent.ID, Kind: agent.Kind, Output: output, TaskID: result.ID, State: result.Status.State}, nil
}

func partsText(parts []a2aPart) string {
	var out strings.Builder
	for _, part := range parts {
		out.WriteString(part.Text)
	}
	return out.String()
}

func truncateText(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}
