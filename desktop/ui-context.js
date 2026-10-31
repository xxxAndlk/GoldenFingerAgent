'use strict';

const { execFile } = require('node:child_process');

const SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -AssemblyName WindowsBase
$element = [System.Windows.Automation.AutomationElement]::FromPoint((New-Object System.Windows.Point(__X__, __Y__)))
if (-not $element) { exit 0 }
$current = $element.Current
[PSCustomObject]@{
  name = $(if ($current.IsPassword) { '' } else { $current.Name })
  control = $current.ControlType.ProgrammaticName.Replace('ControlType.', '')
  isPassword = [bool]$current.IsPassword
} | ConvertTo-Json -Compress
`;

function parseUiContext(stdout) {
  const text = String(stdout || '').trim();
  if (!text) return {};
  const value = JSON.parse(text);
  return {
    name: String(value.name || '').replace(/\s+/g, ' ').trim().slice(0, 160),
    control: String(value.control || '').slice(0, 60),
    isPassword: Boolean(value.isPassword),
  };
}

function readUiContext(x, y, execFileImpl = execFile) {
  if (process.platform !== 'win32') return Promise.resolve({});
  const script = SCRIPT.replace('__X__', Number(x) || 0).replace('__Y__', Number(y) || 0);
  return new Promise(resolve => {
    execFileImpl('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', script], { timeout: 2500, windowsHide: true, encoding: 'utf8', maxBuffer: 32768 }, (error, stdout) => {
      if (error) return resolve({});
      try { resolve(parseUiContext(stdout)); } catch { resolve({}); }
    });
  });
}

module.exports = { readUiContext, parseUiContext };
