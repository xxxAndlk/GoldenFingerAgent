package agenthub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// parentDir 返回父目录；无法解析时回退到原值（避免空 Dir 让进程跑在随机目录）。
func parentDir(p string) string {
	dir := filepath.Dir(p)
	if dir == "." || dir == "" {
		return p
	}
	return dir
}

func systemProcesses(ctx context.Context) ([]Process, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	script := `Get-Process | Select-Object Name,Path,MainWindowHandle | ConvertTo-Json -Compress`
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(output))
	var processes []Process
	if text != "" {
		if !strings.HasPrefix(text, "[") {
			output = []byte("[" + text + "]")
		}
		if err := json.Unmarshal(output, &processes); err != nil {
			return nil, err
		}
	}
	return append(processes, runningCLIProcesses(ctx)...), nil
}

// runningCLIProcesses 识别 node.exe 背后的真实 Agent，运行状态不依赖命令是否加入 PATH。
func runningCLIProcesses(ctx context.Context) []Process {
	script := `Get-CimInstance Win32_Process -Filter "Name = 'node.exe'" | Select-Object Name,CommandLine | ConvertTo-Json -Compress`
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil || len(strings.TrimSpace(string(output))) == 0 {
		return nil
	}
	text := strings.TrimSpace(string(output))
	if !strings.HasPrefix(text, "[") {
		output = []byte("[" + text + "]")
	}
	var rows []struct {
		CommandLine string `json:"CommandLine"`
	}
	if json.Unmarshal(output, &rows) != nil {
		return nil
	}
	patterns := []struct{ marker, name string }{
		{"@moonshot-ai/kimi-code", "kimi-code"},
		{"@anthropic-ai/claude-code", "claude"},
		{"@mariozechner/pi-coding-agent", "pi"},
		{"hermes-agent", "hermes"},
	}
	var processes []Process
	for _, row := range rows {
		commandLine := strings.ToLower(strings.ReplaceAll(row.CommandLine, `\`, "/"))
		for _, pattern := range patterns {
			if strings.Contains(commandLine, pattern.marker) {
				processes = append(processes, Process{Name: pattern.name})
				break
			}
		}
	}
	return processes
}

// findDesktopExecutable 从开始菜单和桌面快捷方式定位已安装但未运行的桌面 Agent。
func findDesktopExecutable(ctx context.Context, entry catalogEntry) string {
	names, _ := json.Marshal(append([]string{entry.name}, entry.processNames...))
	encoded := base64.StdEncoding.EncodeToString(names)
	script := fmt.Sprintf(`$candidates = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(%q)) | ConvertFrom-Json
$roots = @("$env:APPDATA\Microsoft\Windows\Start Menu\Programs", "$env:ProgramData\Microsoft\Windows\Start Menu\Programs", "$env:USERPROFILE\Desktop")
$shell = New-Object -ComObject WScript.Shell
Get-ChildItem $roots -Filter *.lnk -Recurse -ErrorAction SilentlyContinue | ForEach-Object {
  $shortcut = $_
  foreach ($candidate in $candidates) {
    if ($shortcut.BaseName -imatch [regex]::Escape([string]$candidate)) {
      $target = $shell.CreateShortcut($shortcut.FullName).TargetPath
      if ($target -and [IO.Path]::GetExtension($target) -eq '.exe' -and (Test-Path $target -PathType Leaf)) {
        Write-Output $target
        exit 0
      }
    }
  }
}
`, encoded)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(output))
	if !fileExists(path) {
		return ""
	}
	return path
}

// openDesktop 启动未运行的软件；已运行时只唤起现有主窗口，不重复启动实例。
func openDesktop(ctx context.Context, agent Agent) (Invocation, error) {
	if !fileExists(agent.Executable) {
		return Invocation{}, fmt.Errorf("%s 的桌面入口已失效，请重新扫描", agent.Name)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(agent.Executable))
	script := fmt.Sprintf(`$exe = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(%q))
Add-Type @"
using System;
using System.Runtime.InteropServices;
public static class GFAOpenWindow {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int command);
}
"@
$process = Get-Process | Where-Object { $_.Path -eq $exe -and $_.MainWindowHandle -ne 0 } | Select-Object -First 1
if ($process) {
  [GFAOpenWindow]::ShowWindow($process.MainWindowHandle, 9) | Out-Null
  [GFAOpenWindow]::SetForegroundWindow($process.MainWindowHandle) | Out-Null
  Write-Output 'focused'
} else {
  Start-Process -FilePath $exe -WorkingDirectory (Split-Path -Parent $exe)
  Write-Output 'opened'
}
`, encoded)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script).CombinedOutput()
	if err != nil {
		return Invocation{}, fmt.Errorf("打开 %s 失败：%w：%s", agent.Name, err, strings.TrimSpace(string(output)))
	}
	state := strings.TrimSpace(string(output))
	if state != "focused" {
		state = "opened"
	}
	message := "已打开 " + agent.Name
	if state == "focused" {
		message = "已唤起 " + agent.Name + " 窗口"
	}
	return Invocation{AgentID: agent.ID, Kind: agent.Kind, Output: message, State: state}, nil
}

func (m *Manager) invokeDesktop(ctx context.Context, agent Agent, task string) (Invocation, error) {
	if agent.Executable == "" {
		return Invocation{}, fmt.Errorf("%s 没有可定位的桌面窗口", agent.Name)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(task))
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName Microsoft.VisualBasic
Add-Type @"
using System;
using System.Runtime.InteropServices;
public static class GFAWindow {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int command);
}
"@
$process = Get-Process | Where-Object { $_.Path -eq %q -and $_.MainWindowHandle -ne 0 } | Select-Object -First 1
if (-not $process) { throw '找不到可操作的桌面窗口' }
[GFAWindow]::ShowWindow($process.MainWindowHandle, 9) | Out-Null
[GFAWindow]::SetForegroundWindow($process.MainWindowHandle) | Out-Null
Start-Sleep -Milliseconds 500
$text = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(%q))
$old = [Windows.Clipboard]::GetText()
try {
  [Windows.Clipboard]::SetText($text)
  [Windows.Forms.SendKeys]::SendWait('^v')
  Start-Sleep -Milliseconds 300
  [Windows.Forms.SendKeys]::SendWait('{ENTER}')
} finally {
  if ($old) { [Windows.Clipboard]::SetText($old) }
}
`, agent.Executable, encoded)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "powershell.exe", "-STA", "-NoProfile", "-Command", script).CombinedOutput()
	if err != nil {
		return Invocation{}, fmt.Errorf("桌面提交失败：%w：%s", err, strings.TrimSpace(string(output)))
	}
	return Invocation{AgentID: agent.ID, Kind: agent.Kind, Output: "任务已提交到 " + agent.Name + " 桌面窗口；执行结果可由协作模式继续记录。", State: "submitted"}, nil
}
