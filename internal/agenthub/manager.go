// Package agenthub 统一发现、调用和记录本机及 A2A Agent。
package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Agent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Installed    bool     `json:"installed"`
	Running      bool     `json:"running"`
	Executable   string   `json:"executable,omitempty"`
	Endpoint     string   `json:"endpoint,omitempty"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
	Detail       string   `json:"detail,omitempty"`
	// Weight 是未被显式指定目标时的派发优先级（越大越优先）。
	Weight int `json:"weight"`
}

type Process struct {
	Name             string `json:"Name"`
	Path             string `json:"Path"`
	MainWindowHandle int64  `json:"MainWindowHandle"`
}

type Endpoint struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

type endpointFile struct {
	A2A []Endpoint `json:"a2a"`
}

type Invocation struct {
	AgentID string `json:"agent_id"`
	Kind    string `json:"kind"`
	Output  string `json:"output"`
	TaskID  string `json:"task_id,omitempty"`
	State   string `json:"state,omitempty"`
}

type Manager struct {
	Workspace  string
	ConfigPath string
	BMAURL     string
	// BMAPath 是桌面版 app\launch.ps1 或 bma-server.exe 的完整路径；
	// BMA 未运行时据此直接拉起，不再依赖「端口是否可连」的猜测。
	BMAPath string
	// BMAToken 是安装目录 .env 里的 BMA_API_TOKEN（启用鉴权时使用）。
	BMAToken string
	// BMALaunch 是拉起实现（可注入以便测试）；为 nil 时使用系统默认实现。
	BMALaunch   func(context.Context, string) error
	HTTP        *http.Client
	LookPath    func(string) (string, error)
	ProbeCLI    func(context.Context, string, string) (string, error)
	Processes   func(context.Context) ([]Process, error)
	FindDesktop func(context.Context, catalogEntry) string
	OpenDesktop func(context.Context, Agent) (Invocation, error)
	OpenCLI     func(context.Context, Agent, string) (Invocation, error)
	CCSwitch    *CCSwitch
}

type catalogEntry struct {
	id, name, kind, command, description string
	processNames                         []string
	args                                 []string
	output                               string
}

var catalog = []catalogEntry{
	{id: "claude-code", name: "Claude Code", kind: "cli", command: "claude", processNames: []string{"claude"}, args: []string{"--print", "--output-format", "json"}, output: "json", description: "命令行编程 Agent，使用 claude --print 非交互调用"},
	{id: "pi", name: "Pi", kind: "cli", command: "pi", processNames: []string{"pi"}, args: []string{"--print"}, description: "Pi 编程 Agent，使用 pi --print 非交互调用"},
	{id: "chatgpt", name: "ChatGPT", kind: "desktop", processNames: []string{"ChatGPT"}, description: "桌面 Agent，可启动、唤起并通过本机窗口提交任务"},
	{id: "hermes", name: "Hermes", kind: "cli", command: "hermes", processNames: []string{"hermes"}, args: []string{"-p"}, description: "Hermes Agent 与 CC Switch 供应商配置"},
	{id: "deepseek-harness", name: "DeepSeek Harness", kind: "desktop", command: "deepseek", processNames: []string{"DeepSeekHarness", "DeepSeek Harness", "deepseek-harness"}, description: "DeepSeek Harness 命令行或桌面 Agent"},
	{id: "kimi-code", name: "Kimi Code", kind: "cli", command: "kimi", processNames: []string{"KimiCode", "kimi-code"}, args: []string{"-p"}, description: "Kimi Code 编程 Agent"},
	{id: "kimiwork", name: "KimiWork", kind: "desktop", processNames: []string{"Kimi"}, description: "KimiWork 桌面 Agent，可启动、唤起并通过本机窗口提交任务"},
	{id: "workbuddy", name: "WorkBuddy", kind: "desktop", processNames: []string{"WorkBuddy"}, description: "WorkBuddy 桌面 Agent，可启动、唤起并通过本机窗口提交任务"},
	{id: "qoder", name: "Qoder", kind: "desktop", command: "qoder", processNames: []string{"Qoder", "Qoder Launcher"}, description: "Qoder 桌面编程 Agent，可启动、唤起并分派任务"},
	{id: "ccswitch", name: "CC Switch", kind: "ccswitch", command: "ccswitch", processNames: []string{"CC Switch", "cc-switch", "ccswitch"}, description: "读取供应商、用量和额度；Claude Code 支持安全切换"},
	{id: "bma", name: "BMA", kind: "bma", command: "bma-server", processNames: []string{"bma-server"}, description: "当前项目的专属 BMA Agent；优先使用原生 API，也可用 A2A 覆盖"},
}

func New(workspace, configPath string) *Manager {
	home, _ := os.UserHomeDir()
	installURL, installLauncher, installToken := discoverBMAInstall(home)
	bmaURL := strings.TrimSpace(os.Getenv("GFA_BMA_URL"))
	if bmaURL == "" {
		bmaURL = installURL
	}
	if bmaURL == "" {
		bmaURL = "http://127.0.0.1:10010"
	}
	bmaPath := strings.TrimSpace(os.Getenv("GFA_BMA_PATH"))
	if bmaPath == "" {
		bmaPath = installLauncher
	}
	if bmaPath == "" {
		bmaPath = defaultBMAPath(home)
	}
	bmaToken := strings.TrimSpace(os.Getenv("GFA_BMA_TOKEN"))
	if bmaToken == "" {
		bmaToken = installToken
	}
	return &Manager{
		Workspace: workspace, ConfigPath: configPath, BMAURL: bmaURL,
		BMAPath: bmaPath, BMAToken: bmaToken, BMALaunch: launchDetachedProcess,
		HTTP: &http.Client{Timeout: 120 * time.Second}, LookPath: lookCLIPath, ProbeCLI: probeCLI,
		Processes: systemProcesses, FindDesktop: findDesktopExecutable, OpenDesktop: openDesktop, OpenCLI: openCLI,
		CCSwitch: NewCCSwitch(filepath.Join(home, ".cc-switch", "cc-switch.db"), home),
	}
}

// defaultBMAPath 猜测本机 BMA 可执行文件的位置（可用 GFA_BMA_PATH 覆盖）。
func defaultBMAPath(home string) string {
	candidates := []string{
		`D:\WebApp\bma\bin\bma-server.exe`,
		filepath.Join(home, "WebApp", "bma", "bin", "bma-server.exe"),
		filepath.Join(home, "bma", "bin", "bma-server.exe"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

// discoverBMAInstall 查找桌面版安装目录，读取 .env 的端口与 Token。
// 桌面版布局：<root>\app\launch.ps1 + <root>\.env。
func discoverBMAInstall(home string) (baseURL, launcher, token string) {
	roots := []string{strings.TrimSpace(os.Getenv("GFA_BMA_HOME"))}
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		roots = append(roots, filepath.Join(local, "Programs", "BlockMemoryAgent"))
	}
	roots = append(roots,
		`D:\WebApp\BMA-Desktop`,
		`D:\WebApp\bma`,
		filepath.Join(home, "WebApp", "bma"),
		filepath.Join(home, "bma"),
	)
	for _, root := range roots {
		if root == "" {
			continue
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		envPath := filepath.Join(root, ".env")
		baseURL = normalizeBMAAddr(readEnvValue(envPath, "HTTP_ADDR"))
		token = strings.TrimSpace(readEnvValue(envPath, "BMA_API_TOKEN"))
		for _, candidate := range []string{
			filepath.Join(root, "app", "launch.ps1"),
			filepath.Join(root, "BMA-Desktop.exe"),
			filepath.Join(root, "bin", "bma-server.exe"),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				launcher = candidate
				break
			}
		}
		if launcher != "" || baseURL != "" {
			return baseURL, launcher, token
		}
	}
	return "", "", ""
}

func readEnvValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) == key {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

func normalizeBMAAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	return strings.TrimRight(addr, "/")
}

func (m *Manager) endpoints() ([]Endpoint, error) {
	if m.ConfigPath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(m.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file endpointFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("解析 Agent 配置：%w", err)
	}
	return file.A2A, nil
}

func (m *Manager) RegisterA2A(endpoint Endpoint) (Agent, error) {
	endpoint.ID = strings.TrimSpace(endpoint.ID)
	endpoint.Name = strings.TrimSpace(endpoint.Name)
	endpoint.URL = strings.TrimRight(strings.TrimSpace(endpoint.URL), "/")
	if endpoint.URL == "" || (!strings.HasPrefix(endpoint.URL, "http://") && !strings.HasPrefix(endpoint.URL, "https://")) {
		return Agent{}, errors.New("A2A URL 必须是 http 或 https 地址")
	}
	if endpoint.ID == "" {
		endpoint.ID = "a2a-" + fmt.Sprintf("%x", time.Now().UnixNano())
	}
	if endpoint.Name == "" {
		endpoint.Name = endpoint.ID
	}
	items, err := m.endpoints()
	if err != nil {
		return Agent{}, err
	}
	replaced := false
	for i := range items {
		if items[i].ID == endpoint.ID {
			items[i], replaced = endpoint, true
			break
		}
	}
	if !replaced {
		items = append(items, endpoint)
	}
	raw, _ := json.MarshalIndent(endpointFile{A2A: items}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(m.ConfigPath), 0o700); err != nil {
		return Agent{}, err
	}
	tmp := m.ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return Agent{}, err
	}
	if err := os.Rename(tmp, m.ConfigPath); err != nil {
		return Agent{}, err
	}
	return m.discoverA2A(context.Background(), endpoint), nil
}

func (m *Manager) Scan(ctx context.Context) []Agent {
	processes, processErr := m.Processes(ctx)
	endpoints, endpointErr := m.endpoints()
	agents := make([]Agent, 0, len(catalog)+len(endpoints))
	for _, entry := range catalog {
		agent := Agent{ID: entry.id, Name: entry.name, Kind: entry.kind, Description: entry.description, Capabilities: []string{"scan"}}
		cliConflict := false
		cliProbeError := ""
		cliVersion := ""
		if entry.command != "" {
			if path, err := m.LookPath(entry.command); err == nil {
				if entry.kind != "cli" || usableCLIPath(entry, path, processes) {
					agent.Installed = true
					if entry.kind == "cli" && m.ProbeCLI != nil {
						version, probeErr := m.ProbeCLI(ctx, path, m.Workspace)
						if probeErr != nil {
							cliProbeError = probeErr.Error()
						} else {
							agent.Executable, cliVersion = path, version
						}
					} else {
						agent.Executable = path
					}
				} else {
					cliConflict = true
				}
			}
		}
		for _, process := range processes {
			if !matchesProcess(process.Name, entry.processNames) {
				continue
			}
			agent.Running, agent.Installed = true, true
			if process.Path != "" {
				agent.Executable = process.Path
			}
			break
		}
		if entry.kind == "desktop" && agent.Executable == "" && m.FindDesktop != nil {
			if path := m.FindDesktop(ctx, entry); path != "" {
				agent.Installed, agent.Executable = true, path
			}
		}
		switch entry.kind {
		case "cli":
			if cliProbeError != "" {
				agent.Executable = ""
				agent.Detail = "CLI 文件已找到，但命令执行探测失败：" + cliProbeError
			} else if agent.Executable != "" {
				agent.Capabilities = append(agent.Capabilities, "open", "delegate")
				agent.Detail = "命令执行探测通过"
				if cliVersion != "" {
					agent.Detail += "（" + strings.Split(cliVersion, "\n")[0] + "）"
				}
				agent.Detail += "；可启动终端或分派任务"
			} else if cliConflict {
				agent.Detail = "检测到同名桌面软件，但未发现独立 CLI 命令"
			} else {
				agent.Detail = "PATH 与 npm/pnpm 目录均未发现命令入口"
			}
		case "desktop":
			if agent.Executable != "" {
				agent.Capabilities = append(agent.Capabilities, "open")
			}
			if agent.Running {
				agent.Capabilities = append(agent.Capabilities, "desktop_delegate")
				agent.Detail = "桌面已运行，可唤起或分派任务"
			} else if agent.Installed {
				agent.Detail = "已安装，可打开桌面版后分派任务"
			} else {
				agent.Detail = "未发现桌面入口"
			}
		case "bma":
			if fileExists(filepath.Join(m.Workspace, ".bma", "PROJECT.md")) {
				agent.Installed = true
			}
			agent.Capabilities = append(agent.Capabilities, "project")
			agent.Detail = "已识别 BMA 项目；正在检查原生 API"
			agent = m.discoverBMA(ctx, agent)
			// BMA 是本机专属 Agent：默认派发优先级最高。
			agent.Weight = 100
		case "ccswitch":
			if m.CCSwitch != nil && fileExists(m.CCSwitch.DBPath) {
				agent.Installed = true
				agent.Capabilities = append(agent.Capabilities, "providers", "quota", "switch_claude")
			}
		}
		agents = append(agents, agent)
	}
	for _, endpoint := range endpoints {
		agents = append(agents, m.discoverA2A(ctx, endpoint))
	}
	if processErr != nil && len(agents) > 0 {
		agents[0].Detail = "进程扫描受限：" + processErr.Error()
	}
	if endpointErr != nil {
		agents = append(agents, Agent{ID: "config-error", Name: "Agent 配置", Kind: "error", Description: endpointErr.Error()})
	}
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Running != agents[j].Running {
			return agents[i].Running
		}
		if agents[i].Installed != agents[j].Installed {
			return agents[i].Installed
		}
		return agents[i].Name < agents[j].Name
	})
	return agents
}

func matchesProcess(name string, candidates []string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, candidate := range candidates {
		if name == strings.ToLower(candidate) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool { info, err := os.Stat(path); return err == nil && !info.IsDir() }

func (m *Manager) Delegate(ctx context.Context, agentID, task string) (Invocation, error) {
	return m.DelegateAt(ctx, agentID, task, "")
}

// DelegateAuto 在调用方没有指定 agent_id 时，按权重挑选可派发的 Agent。
// BMA 权重最高；同权重时按扫描顺序（稳定），并跳过不可派发的目标。
func (m *Manager) DelegateAuto(ctx context.Context, task, workDir string) (Invocation, error) {
	agents := m.Scan(ctx)
	best := Agent{}
	found := false
	for _, candidate := range agents {
		if !candidate.Running {
			continue
		}
		if !hasCapability(candidate, "delegate") && candidate.Kind != "bma" {
			continue
		}
		// 显式要求未指定：按权重挑最大的。
		if !found || candidate.Weight > best.Weight {
			best, found = candidate, true
		}
	}
	if !found {
		return Invocation{}, fmt.Errorf("没有可派发的 Agent（请先确认 BMA 等目标已安装并在运行）")
	}
	return m.DelegateAt(ctx, best.ID, task, workDir)
}

func hasCapability(a Agent, name string) bool {
	for _, capability := range a.Capabilities {
		if capability == name {
			return true
		}
	}
	return false
}

func (m *Manager) resolveWorkDir(requested string) (string, error) {
	workDir := strings.TrimSpace(requested)
	if workDir == "" {
		workDir = m.Workspace
	}
	absolute, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("工作目录无效：%w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("工作目录不可访问：%w", err)
	}
	if !info.IsDir() {
		return "", errors.New("工作目录必须是文件夹，不能是文件")
	}
	return absolute, nil
}

func taskWithWorkDir(task, workDir string) string {
	return "工作目录：" + workDir + "\n请在这个目录内完成工作；如需修改其他位置，先说明。\n\n" + task
}

func (m *Manager) DelegateAt(ctx context.Context, agentID, task, requestedWorkDir string) (Invocation, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return Invocation{}, errors.New("任务不能为空")
	}
	if len(task) > 12000 {
		return Invocation{}, errors.New("任务不能超过 12000 字符")
	}
	workDir, err := m.resolveWorkDir(requestedWorkDir)
	if err != nil {
		return Invocation{}, err
	}
	endpoints, err := m.endpoints()
	if err != nil {
		return Invocation{}, err
	}
	for _, endpoint := range endpoints {
		if endpoint.ID != agentID {
			continue
		}
		agent := m.discoverA2A(ctx, endpoint)
		if !agent.Running {
			return Invocation{}, fmt.Errorf("%s 的 A2A 服务不可用：%s", agent.Name, agent.Detail)
		}
		return m.invokeA2A(ctx, agent, taskWithWorkDir(task, workDir))
	}
	for _, agent := range m.Scan(ctx) {
		if agent.ID != agentID {
			continue
		}
		if agent.Kind == "a2a" {
			return m.invokeA2A(ctx, agent, taskWithWorkDir(task, workDir))
		}
		if agent.Kind == "bma" && agent.Endpoint != "" {
			return m.invokeBMA(ctx, agent, task, workDir)
		}
		for _, entry := range catalog {
			if entry.id != agentID {
				continue
			}
			if entry.kind == "cli" && agent.Executable != "" {
				return m.invokeCLI(ctx, agent, entry, task, workDir)
			}
			if entry.kind == "desktop" && agent.Running {
				return m.invokeDesktop(ctx, agent, taskWithWorkDir(task, workDir))
			}
		}
		if agentID == "bma" {
			return Invocation{}, errors.New("BMA 原生 API 不可用；请启动 BMA 服务，或在 Agent 面板配置 A2A 端点")
		}
		return Invocation{}, fmt.Errorf("%s 当前没有可用的任务调用通道", agent.Name)
	}
	return Invocation{}, errors.New("未找到该 Agent")
}

func (m *Manager) Open(ctx context.Context, agentID string) (Invocation, error) {
	for _, agent := range m.Scan(ctx) {
		if agent.ID != agentID || agent.Executable == "" {
			continue
		}
		switch agent.Kind {
		case "desktop":
			if m.OpenDesktop == nil {
				return Invocation{}, errors.New("当前系统不支持桌面启动")
			}
			return m.OpenDesktop(ctx, agent)
		case "cli":
			if m.OpenCLI == nil {
				return Invocation{}, errors.New("当前系统不支持 CLI 终端启动")
			}
			workDir, err := m.resolveWorkDir("")
			if err != nil {
				return Invocation{}, err
			}
			return m.OpenCLI(ctx, agent, workDir)
		}
	}
	return Invocation{}, errors.New("未找到可启动的 Agent；请确认桌面软件或独立 CLI 已安装")
}

func cliCommandArgs(entry catalogEntry, task string) []string {
	return append(append([]string(nil), entry.args...), task)
}

func parseCLIOutput(agent Agent, raw string, format string) Invocation {
	result := Invocation{AgentID: agent.ID, Kind: agent.Kind, Output: strings.TrimSpace(raw), State: "completed"}
	if format == "json" && agent.ID == "claude-code" {
		var payload struct {
			Result    string `json:"result"`
			SessionID string `json:"session_id"`
			IsError   bool   `json:"is_error"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			result.Output = strings.TrimSpace(payload.Result)
			result.TaskID = payload.SessionID
			if payload.IsError {
				result.State = "error"
			}
		}
	}
	if len(result.Output) > 50000 {
		result.Output = result.Output[:50000] + "…"
	}
	return result
}

func (m *Manager) invokeCLI(ctx context.Context, agent Agent, entry catalogEntry, task, workDir string) (Invocation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	output, err := executeCLI(ctx, agent.Executable, cliCommandArgs(entry, task), workDir)
	raw := strings.TrimSpace(string(output))
	if err != nil {
		return Invocation{}, fmt.Errorf("%s 命令执行失败：%w：%s", agent.Name, err, truncateText(raw, 5000))
	}
	result := parseCLIOutput(agent, raw, entry.output)
	if result.State == "error" {
		return Invocation{}, fmt.Errorf("%s 返回错误：%s", agent.Name, result.Output)
	}
	return result, nil
}
