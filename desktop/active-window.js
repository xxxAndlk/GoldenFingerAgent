'use strict';

const { execFile } = require('node:child_process');

// 仅查询当前前台窗口的标题和进程名，不读取屏幕像素、文件或输入内容。
const WINDOWS_SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
try {
  Add-Type -AssemblyName UIAutomationClient -ErrorAction Stop
  Add-Type -AssemblyName UIAutomationTypes -ErrorAction Stop
} catch { exit 0 }
# 使用 UIAutomation 的焦点元素向上找顶层窗口。低完整性进程
# （例如 --no-sandbox 启动）不允许 Add-Type 编译 C#，但可以加载 UIAutomation。
$focused = [System.Windows.Automation.AutomationElement]::FocusedElement
if (-not $focused) { exit 0 }
$walker = [System.Windows.Automation.TreeWalker]::ControlViewWalker
$window = $focused
while ($window -and $window.Current.ControlType.ProgrammaticName -ne 'ControlType.Window') {
  $window = $walker.GetParent($window)
}
$title = if ($window) { $window.Current.Name } else { $focused.Current.Name }
$processId = $focused.Current.ProcessId
$process = Get-Process -Id $processId -ErrorAction SilentlyContinue
[PSCustomObject]@{
  title = $title
  processId = $processId
  owner = @{ name = $(if ($process) { $process.ProcessName } else { '' }) }
} | ConvertTo-Json -Compress
`;

function readActiveWindow(execFileImpl = execFile) {
  if (process.platform !== 'win32') {
    return Promise.reject(new Error('当前桌面协作扫描仅支持 Windows'));
  }
  return new Promise((resolve, reject) => {
    execFileImpl(
      'powershell.exe',
      ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', WINDOWS_SCRIPT],
      { timeout: 5000, windowsHide: true, encoding: 'utf8', maxBuffer: 64 * 1024 },
      (error, stdout) => {
        if (error) return reject(error);
        try {
          resolve(parseActiveWindowOutput(stdout));
        } catch (parseError) {
          reject(parseError);
        }
      },
    );
  });
}

// 高权限应用的进程名读不到时，从窗口标题尾部推断应用名。
function appFromTitle(title) {
  const cleanTitle = String(title || '').replace(/\s*\[[^\]]*\]\s*$/, '').trim();
  const parts = cleanTitle.split(/\s+-\s+/);
  return parts.length > 1 ? parts[parts.length - 1].trim() : '';
}

function parseActiveWindowOutput(stdout) {
  const text = String(stdout || '').trim();
  if (!text) return null;
  const value = JSON.parse(text);
  const title = String(value.title || '');
  const ownerName = String((value.owner && value.owner.name) || '').trim();
  return {
    title,
    owner: { name: ownerName || appFromTitle(title) || '未知应用' },
  };
}

module.exports = { readActiveWindow, parseActiveWindowOutput, appFromTitle };
