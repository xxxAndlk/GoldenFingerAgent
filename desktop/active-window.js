'use strict';

const { execFile } = require('node:child_process');

// 仅查询当前前台窗口的标题和进程名，不读取屏幕像素、文件或输入内容。
const WINDOWS_SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type @"
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class GoldenFingerForeground {
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int count);
  [DllImport("user32.dll")] public static extern int GetWindowTextLength(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);
}
"@
$handle = [GoldenFingerForeground]::GetForegroundWindow()
if ($handle -eq [IntPtr]::Zero) { exit 0 }
$length = [GoldenFingerForeground]::GetWindowTextLength($handle)
$builder = New-Object System.Text.StringBuilder ($length + 1)
[void][GoldenFingerForeground]::GetWindowText($handle, $builder, $builder.Capacity)
$processId = 0
[void][GoldenFingerForeground]::GetWindowThreadProcessId($handle, [ref]$processId)
$process = Get-Process -Id $processId -ErrorAction SilentlyContinue
[PSCustomObject]@{
  title = $builder.ToString()
  owner = @{ name = $(if ($process) { $process.ProcessName } else { '未知应用' }) }
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

function parseActiveWindowOutput(stdout) {
  const text = String(stdout || '').trim();
  if (!text) return null;
  const value = JSON.parse(text);
  return {
    title: String(value.title || ''),
    owner: { name: String(value.owner && value.owner.name || '未知应用') },
  };
}

module.exports = { readActiveWindow, parseActiveWindowOutput };
