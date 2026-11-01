package agenthub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// BMA 的会话是异步的：POST /api/sessions 只代表「已受理」，
// 真正的结果要按 id 轮询 GET /api/sessions/{id} 才能拿到。
// 旧实现只读一次 POST 响应，所以永远只有「已接收任务」，看不到结果。

const (
	bmaPollInterval = 1500 * time.Millisecond
	// bmaMaxWait 是单次派发的墙钟上限（BMA 侧任务可能跑十几分钟）。
	bmaMaxWait = 30 * time.Minute
	// bmaIdleTimeout 是连续多久拿不到状态推进就放弃轮询。
	bmaIdleTimeout = 5 * time.Minute
)

// discoverBMA 检查 BMA 原生 API。进程发现只代表已安装，健康检查通过后才开放分派。
func (m *Manager) discoverBMA(parent context.Context, agent Agent) Agent {
	base := strings.TrimRight(strings.TrimSpace(m.BMAURL), "/")
	if base == "" {
		return agent
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/health", nil)
	if err != nil {
		agent.Detail = "BMA 地址无效：" + err.Error()
		return m.markBMAInstalled(agent)
	}
	m.authorizeBMA(req)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		agent.Detail = "已安装桌面版 BMA，但当前未运行；首次分派会自动启动"
		return m.markBMAInstalled(agent)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		agent.Detail = fmt.Sprintf("BMA 原生 API HTTP %d", resp.StatusCode)
		return m.markBMAInstalled(agent)
	}
	agent.Installed = true
	agent.Running = true
	agent.Endpoint = base + "/api"
	agent.Executable = m.BMAPath
	agent.Capabilities = append(agent.Capabilities, "delegate", "models")
	agent.Detail = "BMA 桌面版原生 API 已连接；任务结果会在完成后回传"
	return agent
}

// markBMAInstalled 让「已安装但未运行」的 BMA 仍出现在 Agent 列表里；
// 分派时 ensureBMARunning 会调用桌面版 launch.ps1。
func (m *Manager) markBMAInstalled(agent Agent) Agent {
	if strings.TrimSpace(m.BMAPath) == "" {
		return agent
	}
	if info, err := os.Stat(m.BMAPath); err != nil || info.IsDir() {
		return agent
	}
	agent.Installed = true
	agent.Executable = m.BMAPath
	agent.Capabilities = append(agent.Capabilities, "delegate", "models")
	agent.Detail = "已安装桌面版 BMA；分派任务时会自动启动"
	return agent
}

func (m *Manager) authorizeBMA(req *http.Request) {
	if m.BMAToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.BMAToken)
	}
}

type bmaSession struct {
	ID      string           `json:"id"`
	Goal    string           `json:"goal"`
	Status  string           `json:"status"`
	Result  string           `json:"result"`
	WorkDir string           `json:"work_dir"`
	State   *bmaSessionState `json:"state,omitempty"`
}

// bmaSessionState 只解析调度需要的部分；BMA 还会返回 active_blocks 等字段。
type bmaSessionState struct {
	PendingClarify *bmaClarify `json:"pending_clarify,omitempty"`
}

// bmaClarify 是 BMA ask_user / 计划确认等待用户答复的上下文。
type bmaClarify struct {
	ID       string             `json:"id"`
	Question string             `json:"question"`
	Context  string             `json:"context,omitempty"`
	AgentID  string             `json:"agent_id,omitempty"`
	Kind     string             `json:"kind,omitempty"`
	Detail   string             `json:"detail,omitempty"`
	Options  []bmaClarifyOption `json:"options,omitempty"`
}

type bmaClarifyOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// bmaTerminal 判断会话是否已经结束。
func bmaTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "done", "success", "succeeded", "failed", "error", "cancelled", "canceled":
		return true
	}
	return false
}

func bmaFailed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "cancelled", "canceled":
		return true
	}
	return false
}

// bmaNeedsInput 表示会话没有结束，但在等用户答复或人工恢复。
// 继续轮询只会把整次 HTTP 派发拖到超时，必须立即把问题交回调用方。
func bmaNeedsInput(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "awaiting_clarify", "awaiting_input", "needs_input", "waiting_input", "paused":
		return true
	}
	return false
}

func bmaClarifyInvocation(agent Agent, session bmaSession, sessionID string) Invocation {
	inv := Invocation{
		AgentID: agent.ID,
		Kind:    agent.Kind,
		TaskID:  sessionID,
		State:   "awaiting_clarify",
	}
	var pc *bmaClarify
	if session.State != nil {
		pc = session.State.PendingClarify
	}
	if pc == nil {
		inv.Output = fmt.Sprintf("BMA 会话 %s 已暂停，等待人工处理。", sessionID)
		return inv
	}
	inv.Question = pc.Question
	inv.QuestionID = pc.ID
	var lines []string
	lines = append(lines, "BMA 正在等待你的答复：", pc.Question)
	if strings.TrimSpace(pc.Detail) != "" && strings.TrimSpace(pc.Detail) != strings.TrimSpace(pc.Question) {
		lines = append(lines, "", "计划/上下文：", pc.Detail)
	}
	if len(pc.Options) > 0 {
		lines = append(lines, "", "可选答复：")
		for _, option := range pc.Options {
			label := option.Label
			if option.Description != "" {
				label += "（" + option.Description + "）"
			}
			lines = append(lines, "- "+option.ID+"："+label)
			inv.Options = append(inv.Options, InvocationOption{
				ID: option.ID, Label: option.Label, Description: option.Description,
			})
		}
	}
	lines = append(lines, "", "会话 ID："+sessionID+"；在 Agent 调度结果里直接选择或输入答复即可继续。")
	inv.Output = strings.Join(lines, "\n")
	return inv
}

// ensureBMARunning 在派发前确保 BMA 在线：先探健康检查，不通就用配置的
// 可执行文件直接拉起（不再靠「端口通不通」猜测），最后再探一次。
func (m *Manager) ensureBMARunning(ctx context.Context) error {
	base := strings.TrimRight(strings.TrimSpace(m.BMAURL), "/")
	if base == "" {
		return fmt.Errorf("未配置 BMA 地址")
	}
	if m.bmaHealthy(ctx) {
		return nil
	}
	if strings.TrimSpace(m.BMAPath) == "" {
		return fmt.Errorf("BMA 未运行，且没有配置可执行文件路径")
	}
	if _, err := os.Stat(m.BMAPath); err != nil {
		return fmt.Errorf("BMA 可执行文件不存在：%s", m.BMAPath)
	}
	launch := m.BMALaunch
	if launch != nil {
		if err := launch(ctx, m.BMAPath); err != nil {
			return fmt.Errorf("启动 BMA 失败：%w", err)
		}
	}
	// 给进程启动留出时间，最多等 20 秒。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if m.bmaHealthy(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("BMA 启动后健康检查仍未通过")
}

func (m *Manager) bmaHealthy(ctx context.Context) bool {
	base := strings.TrimRight(strings.TrimSpace(m.BMAURL), "/")
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodGet, base+"/api/health", nil)
	if err != nil {
		return false
	}
	m.authorizeBMA(req)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<18))
	return resp.StatusCode == http.StatusOK
}

// invokeBMA 使用 BMA 的原生项目会话接口，并等待会话真正结束再取回结果。
func (m *Manager) invokeBMA(ctx context.Context, agent Agent, task, workDir string) (Invocation, error) {
	base := strings.TrimRight(agent.Endpoint, "/")
	if base == "" {
		return Invocation{}, fmt.Errorf("BMA 原生 API 地址为空")
	}
	if err := m.ensureBMARunning(ctx); err != nil {
		return Invocation{}, err
	}

	payload, err := json.Marshal(map[string]any{
		"goal":     task,
		"work_dir": workDir,
		"gear":     "daily",
	})
	if err != nil {
		return Invocation{}, err
	}
	submitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(submitCtx, http.MethodPost, base+"/sessions", bytes.NewReader(payload))
	if err != nil {
		return Invocation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	m.authorizeBMA(req)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return Invocation{}, fmt.Errorf("BMA 调用失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return Invocation{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Invocation{}, fmt.Errorf("BMA HTTP %d：%s", resp.StatusCode, truncateText(string(body), 1000))
	}
	var accepted bmaSession
	if err := json.Unmarshal(body, &accepted); err != nil {
		return Invocation{}, fmt.Errorf("BMA 响应解析失败：%w", err)
	}

	sessionID := strings.TrimSpace(accepted.ID)
	if sessionID == "" {
		// 没有 id 就无法回查：把已拿到的内容如实返回，不假装完成。
		return Invocation{
			AgentID: agent.ID, Kind: agent.Kind, State: accepted.Status,
			Output: strings.TrimSpace(accepted.Result),
		}, nil
	}
	if bmaTerminal(accepted.Status) && strings.TrimSpace(accepted.Result) != "" {
		return bmaInvocation(agent, accepted), nil
	}
	return m.waitBMASession(ctx, agent, base, sessionID, accepted.Status)
}

// bmaPlanApprovalAnswer 只识别 BMA 的“计划确认”门，不自动回答普通 ask_user 问题。
func bmaPlanApprovalAnswer(inv Invocation) (string, bool) {
	if inv.State != "awaiting_clarify" {
		return "", false
	}
	text := inv.Question + "\n" + inv.Output
	if !strings.Contains(text, "计划") &&
		!strings.Contains(text, "批准开工") &&
		!strings.Contains(text, "计划待批准") {
		return "", false
	}
	for _, option := range inv.Options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		label := strings.TrimSpace(option.Label)
		if strings.EqualFold(id, "approve") ||
			strings.Contains(label, "批准") ||
			strings.Contains(label, "允许开工") {
			return id, true
		}
	}
	return "", false
}

// autoApproveBMAPlan 连续处理 BMA 的计划确认；普通提问原样返回给调用方。
func (m *Manager) autoApproveBMAPlan(ctx context.Context, inv Invocation) (Invocation, error) {
	answered := make(map[string]bool)
	for i := 0; i < 4; i++ {
		answer, ok := bmaPlanApprovalAnswer(inv)
		if !ok {
			return inv, nil
		}
		if inv.QuestionID != "" && answered[inv.QuestionID] {
			return inv, nil
		}
		if inv.QuestionID != "" {
			answered[inv.QuestionID] = true
		}
		next, err := m.AnswerBMA(ctx, inv.TaskID, answer, inv.QuestionID)
		if err != nil {
			return inv, fmt.Errorf("BMA 计划自动批准失败：%w", err)
		}
		inv = next
	}
	return inv, nil
}

// AnswerBMA 把用户对 BMA 澄清/计划确认的答复发回会话，并继续等待到终态。
// 返回最终结果，或下一次仍需用户输入的提示（BMA 可以连续提问）。
func (m *Manager) AnswerBMA(ctx context.Context, sessionID, answer, questionID string) (Invocation, error) {
	sessionID = strings.TrimSpace(sessionID)
	answer = strings.TrimSpace(answer)
	if sessionID == "" {
		return Invocation{}, fmt.Errorf("缺少 BMA 会话 ID")
	}
	if answer == "" {
		return Invocation{}, fmt.Errorf("答复内容不能为空")
	}
	base := strings.TrimRight(strings.TrimSpace(m.BMAURL), "/")
	if base == "" {
		return Invocation{}, fmt.Errorf("未配置 BMA 地址")
	}
	if err := m.ensureBMARunning(ctx); err != nil {
		return Invocation{}, err
	}
	endpoint := base + "/api"
	if err := m.postBMAClarify(ctx, endpoint, sessionID, answer, questionID); err != nil {
		return Invocation{}, err
	}
	agent := Agent{ID: "bma", Name: "BMA", Kind: "bma", Installed: true, Running: true, Endpoint: endpoint}
	session, err := m.fetchBMASession(ctx, endpoint, sessionID)
	if err != nil {
		// 答复已经受理，查询失败不应伪装成失败。
		return Invocation{
			AgentID: agent.ID, Kind: agent.Kind, TaskID: sessionID, State: "running",
			Output: "答复已提交，BMA 正在继续执行；稍后可在 BMA 会话里查看结果。",
		}, nil
	}
	if (session.State != nil && session.State.PendingClarify != nil) || bmaNeedsInput(session.Status) {
		return bmaClarifyInvocation(agent, session, sessionID), nil
	}
	if bmaTerminal(session.Status) {
		return bmaInvocation(agent, session), nil
	}
	return m.waitBMASession(ctx, agent, endpoint, sessionID, session.Status)
}

func (m *Manager) postBMAClarify(ctx context.Context, endpoint, sessionID, answer, questionID string) error {
	payload, err := json.Marshal(map[string]any{
		"answer":      answer,
		"question_id": questionID,
	})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		endpoint+"/sessions/"+url.PathEscape(sessionID)+"/clarify", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	m.authorizeBMA(req)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("BMA 澄清调用失败：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("BMA 澄清失败 HTTP %d：%s", resp.StatusCode, truncateText(string(body), 500))
	}
	return nil
}

// waitBMASession 轮询会话详情，直到进入终态或超时。
func (m *Manager) waitBMASession(ctx context.Context, agent Agent, base, sessionID, initialStatus string) (Invocation, error) {
	deadline := time.Now().Add(bmaMaxWait)
	last := initialStatus
	lastProgress := time.Now()
	for {
		select {
		case <-ctx.Done():
			return Invocation{}, ctx.Err()
		case <-time.After(bmaPollInterval):
		}
		session, err := m.fetchBMASession(ctx, base, sessionID)
		if err != nil {
			// 单次失败不致命：网络抖动后继续，但超过空闲上限就放弃。
			if time.Since(lastProgress) > bmaIdleTimeout {
				return Invocation{}, fmt.Errorf("BMA 会话 %s 状态查询持续失败：%w", sessionID, err)
			}
			continue
		}
		if session.Status != last {
			last = session.Status
			lastProgress = time.Now()
		}
		if (session.State != nil && session.State.PendingClarify != nil) || bmaNeedsInput(session.Status) {
			return bmaClarifyInvocation(agent, session, sessionID), nil
		}
		if bmaTerminal(session.Status) {
			return bmaInvocation(agent, session), nil
		}
		if time.Since(deadline) > 0 {
			// 超时不伪装成功：明确告知仍在运行，便于上层继续查看。
			return Invocation{
				AgentID: agent.ID, Kind: agent.Kind, TaskID: sessionID, State: session.Status,
				Output: fmt.Sprintf("BMA 仍在执行（会话 %s，状态 %s，已等待 %s）。结果可稍后按会话 ID 查询。",
					sessionID, session.Status, bmaMaxWait),
			}, nil
		}
	}
}

func (m *Manager) fetchBMASession(ctx context.Context, base, sessionID string) (bmaSession, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/sessions/"+sessionID, nil)
	if err != nil {
		return bmaSession{}, err
	}
	m.authorizeBMA(req)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return bmaSession{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return bmaSession{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return bmaSession{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var session bmaSession
	if err := json.Unmarshal(body, &session); err != nil {
		return bmaSession{}, err
	}
	return session, nil
}

// bmaInvocation 把 BMA 会话转换成统一结果；失败状态如实标注。
func bmaInvocation(agent Agent, session bmaSession) Invocation {
	result := strings.TrimSpace(session.Result)
	state := session.Status
	if bmaFailed(session.Status) {
		if result == "" {
			result = "BMA 执行失败（未返回原因）"
		}
		return Invocation{AgentID: agent.ID, Kind: agent.Kind, TaskID: session.ID, State: state, Output: "【BMA 失败】" + result}
	}
	if result == "" {
		result = "BMA 已完成，但没有返回内容"
	}
	return Invocation{AgentID: agent.ID, Kind: agent.Kind, TaskID: session.ID, State: state, Output: result}
}

// fetchBMAHistory 读取最近的会话列表（用于界面「最近任务」与失败排查）。
func (m *Manager) fetchBMAHistory(ctx context.Context, limit int) ([]bmaSession, error) {
	base := strings.TrimRight(strings.TrimSpace(m.BMAURL), "/")
	if base == "" {
		return nil, fmt.Errorf("未配置 BMA 地址")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/api/sessions", nil)
	if err != nil {
		return nil, err
	}
	m.authorizeBMA(req)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var list []bmaSession
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}
