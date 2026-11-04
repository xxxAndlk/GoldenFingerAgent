'use strict';

const { execFile } = require('node:child_process');

// 只通过 UI Automation 枚举顶层窗口，不读取微信数据库、不注入、不操作聊天内容。
// 微信 4.x（Qt）通常只暴露窗口壳，因此这里仅用于定位实际窗口和判断 UIA 是否可用。
const WINDOWS_SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
try {
  Add-Type -AssemblyName UIAutomationClient -ErrorAction Stop
  Add-Type -AssemblyName UIAutomationTypes -ErrorAction Stop
} catch { exit 0 }
$root = [System.Windows.Automation.AutomationElement]::RootElement
$children = $root.FindAll([System.Windows.Automation.TreeScope]::Children, [System.Windows.Automation.Condition]::TrueCondition)
$items = New-Object System.Collections.Generic.List[object]
foreach ($win in $children) {
  try {
    $current = $win.Current
    $process = Get-Process -Id $current.ProcessId -ErrorAction SilentlyContinue
    if (-not $process -or $process.ProcessName -notmatch '^(Weixin|WeChat)$') { continue }
    if ($current.ControlType.ProgrammaticName -ne 'ControlType.Window') { continue }
    if ($current.Name -notmatch '微信') { continue }
    $rect = $current.BoundingRectangle
    if ($rect.Width -le 32 -or $rect.Height -le 32) { continue }
    $childCount = $win.FindAll([System.Windows.Automation.TreeScope]::Children, [System.Windows.Automation.Condition]::TrueCondition).Count
    $items.Add([PSCustomObject]@{
      title = $current.Name
      process = $process.ProcessName
      processId = $current.ProcessId
      handle = [int64]$current.NativeWindowHandle
      offscreen = [bool]$current.IsOffscreen
      childCount = [int]$childCount
      x = [math]::Round($rect.X); y = [math]::Round($rect.Y)
      width = [math]::Round($rect.Width); height = [math]::Round($rect.Height)
    })
  } catch { }
}
if ($items.Count -eq 0) { '[]'; exit 0 }
$items | ConvertTo-Json -Compress -Depth 4
`;

function parseWeChatWindows(stdout) {
  const text = String(stdout || '').trim();
  if (!text) return [];
  let value;
  try { value = JSON.parse(text); } catch { return []; }
  const list = Array.isArray(value) ? value : [value];
  return list.map(item => {
    const width = Number(item && item.width);
    const height = Number(item && item.height);
    if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 32 || height <= 32) return null;
    return {
      title: String((item && item.title) || '微信'),
      process: String((item && item.process) || ''),
      processId: Number(item && item.processId) || 0,
      handle: Number(item && item.handle) || 0,
      offscreen: Boolean(item && item.offscreen),
      childCount: Number(item && item.childCount) || 0,
      rect: { x: Number(item.x), y: Number(item.y), width, height },
    };
  }).filter(Boolean);
}

function readWeChatWindows(execFileImpl = execFile) {
  if (process.platform !== 'win32') return Promise.resolve([]);
  return new Promise(resolve => {
    execFileImpl('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', WINDOWS_SCRIPT], {
      timeout: 5000, windowsHide: true, encoding: 'utf8', maxBuffer: 64 * 1024,
    }, (error, stdout) => {
      if (error) return resolve([]);
      resolve(parseWeChatWindows(stdout));
    });
  });
}

function selectWeChatWindow(windows) {
  const candidates = (windows || []).filter(item => item && !item.offscreen);
  if (!candidates.length) return null;
  return candidates.sort((a, b) =>
    (b.rect.width * b.rect.height) - (a.rect.width * a.rect.height))[0];
}

async function readWeChatWindow(execFileImpl = execFile) {
  return selectWeChatWindow(await readWeChatWindows(execFileImpl));
}

module.exports = { readWeChatWindow, readWeChatWindows, parseWeChatWindows, selectWeChatWindow };
