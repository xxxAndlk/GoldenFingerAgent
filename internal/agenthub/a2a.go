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
	ProtocolVersion     string `json:"protocolVersion,omitempty"`
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
	version := endpoint.ProtocolVersion
	for _, item := range card.SupportedInterfaces {
		if item.URL != "" && (item.ProtocolBinding == "" || strings.EqualFold(item.ProtocolBinding, "JSONRPC")) {
			agent.Endpoint = strings.TrimRight(item.URL, "/")
			if item.ProtocolVersion != "" {
				version = item.ProtocolVersion
			}
			break
		}
	}
	if agent.Endpoint == "" {
		agent.Endpoint = endpoint.URL
	}
	if version == "" {
		version = card.ProtocolVersion
	}
	agent.ProtocolVersion = normalizeA2AVersion(version)
	if agent.ProtocolVersion == "" {
		// 未声明版本时保持旧式 v0.3 报文，不能默认标成 1.0。
		agent.ProtocolVersion = "0.3"
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
	codec := codecFor(agent.ProtocolVersion)
	payload := map[string]any{
		"jsonrpc": "2.0", "id": uuid.NewString(), "method": codec.Method(),
		"params": map[string]any{
			"message":       codec.Message(task),
			"configuration": map[string]any{"blocking": true, "acceptedOutputModes": codec.OutputModes()},
		},
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agent.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return Invocation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", codec.Version())
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
	parsed, err := codec.Parse(body)
	if err != nil {
		return Invocation{}, err
	}
	artifacts, err := m.materializeArtifacts(parsed.Artifacts)
	if err != nil {
		return Invocation{}, err
	}
	output := parsed.Text
	if output == "" && len(artifacts) > 0 {
		output = fmt.Sprintf("外部 Agent 返回了 %d 个文件产物。", len(artifacts))
	}
	if output == "" {
		return Invocation{}, errors.New("A2A 已响应，但没有文本或文件结果")
	}
	return Invocation{
		AgentID: agent.ID, Kind: agent.Kind, Output: output,
		TaskID: parsed.TaskID, State: parsed.State, Artifacts: artifacts,
	}, nil
}

func truncateText(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}
