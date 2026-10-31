package agenthub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// lookCLIPath 先查 PATH，再扫描 npm/pnpm 和用户级目录；跳过已知桌面同名入口继续找 CLI。
func lookCLIPath(command string) (string, error) {
	if path, err := exec.LookPath(command); err == nil {
		normalized := strings.ToLower(filepath.Clean(path))
		if command != "kimi" || !strings.Contains(normalized, `\kimiwork\kimi.exe`) {
			return path, nil
		}
	}
	home, _ := os.UserHomeDir()
	localAppData := os.Getenv("LOCALAPPDATA")
	roots := append(persistentPathDirs(), []string{
		os.Getenv("PNPM_HOME"),
		filepath.Join(localAppData, "pnpm", "bin"),
		filepath.Join(os.Getenv("APPDATA"), "npm"),
		filepath.Join(home, ".npm-global"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".cargo", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"),
	}...)
	// Windows 优先选择可执行入口，避免选中供 Unix shell 使用的无扩展名脚本。
	extensions := []string{".exe", ".cmd", ".bat", ".ps1", ""}
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, extension := range extensions {
			candidate := filepath.Join(root, command+extension)
			if fileExists(candidate) {
				return candidate, nil
			}
		}
	}
	return "", exec.ErrNotFound
}

// probeCLI 不只确认文件存在，还实际执行 --version，排除失效 shim 和错误入口。
func probeCLI(ctx context.Context, executable, workDir string) (string, error) {
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := executeCLI(ctx, executable, []string{"--version"}, workDir)
	if err != nil {
		return "", fmt.Errorf("执行 --version 失败：%w：%s", err, truncateText(strings.TrimSpace(string(output)), 200))
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "退出码 0", nil
	}
	return truncateText(version, 160), nil
}

// usableCLIPath 防止桌面软件与 CLI 同名时，把桌面 exe 当作命令行 Agent。
func usableCLIPath(entry catalogEntry, candidate string, processes []Process) bool {
	normalized := strings.ToLower(filepath.Clean(candidate))
	if entry.id == "kimi-code" && strings.Contains(normalized, `\kimiwork\kimi.exe`) {
		return false
	}
	for _, process := range processes {
		if process.Path == "" || !strings.EqualFold(filepath.Clean(process.Path), normalized) {
			continue
		}
		for _, desktop := range catalog {
			if desktop.kind == "desktop" && matchesProcess(process.Name, desktop.processNames) {
				return false
			}
		}
	}
	return true
}

// executeCLI 直接运行 exe；npm 生成的 cmd/bat 和 PowerShell 入口走无注入的参数化通道。
func executeCLI(ctx context.Context, executable string, args []string, workDir string) ([]byte, error) {
	extension := strings.ToLower(filepath.Ext(executable))
	if runtime.GOOS != "windows" || extension == ".exe" || extension == "" {
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir = workDir
		return cmd.CombinedOutput()
	}
	payload, _ := json.Marshal(map[string]any{"executable": executable, "args": args})
	encoded := base64.StdEncoding.EncodeToString(payload)
	script := fmt.Sprintf(`$spec = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(%q)) | ConvertFrom-Json
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$arguments = @($spec.args)
& $spec.executable @arguments 2>&1
exit $LASTEXITCODE
`, encoded)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Dir = workDir
	return cmd.CombinedOutput()
}

// openCLI 在独立终端中启动交互式 CLI；命令和目录通过 Base64 JSON 传递，避免引号注入。
func openCLI(ctx context.Context, agent Agent, workDir string) (Invocation, error) {
	if runtime.GOOS != "windows" {
		return Invocation{}, fmt.Errorf("交互式 CLI 启动目前仅支持 Windows")
	}
	if !fileExists(agent.Executable) {
		return Invocation{}, fmt.Errorf("%s 的 CLI 入口已失效，请重新扫描", agent.Name)
	}
	if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
		return Invocation{}, fmt.Errorf("CLI 工作目录无效：%s", workDir)
	}
	commandSpec, _ := json.Marshal(map[string]any{"executable": agent.Executable, "workDir": workDir})
	commandEncoded := base64.StdEncoding.EncodeToString(commandSpec)
	inner := fmt.Sprintf(`$spec = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(%q)) | ConvertFrom-Json
[Console]::OutputEncoding = [Text.Encoding]::UTF8
Set-Location -LiteralPath $spec.workDir
& $spec.executable
`, commandEncoded)
	launchSpec, _ := json.Marshal(map[string]any{"inner": inner, "workDir": workDir})
	launchEncoded := base64.StdEncoding.EncodeToString(launchSpec)
	script := fmt.Sprintf(`$spec = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(%q)) | ConvertFrom-Json
$encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($spec.inner))
Start-Process -FilePath 'powershell.exe' -ArgumentList @('-NoExit', '-EncodedCommand', $encoded) -WorkingDirectory $spec.workDir
`, launchEncoded)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script).CombinedOutput()
	if err != nil {
		return Invocation{}, fmt.Errorf("启动 %s 终端失败：%w：%s", agent.Name, err, strings.TrimSpace(string(output)))
	}
	return Invocation{AgentID: agent.ID, Kind: agent.Kind, State: "opened", Output: "已在项目目录启动 " + agent.Name + " 终端"}, nil
}
