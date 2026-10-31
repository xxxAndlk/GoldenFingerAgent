'use strict';

const { spawn } = require('node:child_process');
const readline = require('node:readline');

// 不监听普通字符；仅在 Enter 提交时快照当前非密码编辑框，并报告少数明确快捷键。
const WINDOWS_SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -ReferencedAssemblies @('UIAutomationClient', 'UIAutomationTypes') -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
using System.Text;
using System.Windows.Automation;
public static class GoldenFingerInput {
  private delegate IntPtr HookProc(int code, IntPtr wParam, IntPtr lParam);
  private static HookProc mouseProc;
  private static HookProc keyProc;
  private static IntPtr mouseHook;
  private static IntPtr keyHook;
  [StructLayout(LayoutKind.Sequential)] public struct POINT { public int x; public int y; }
  [StructLayout(LayoutKind.Sequential)] public struct MOUSE { public POINT pt; public uint data; public uint flags; public uint time; public IntPtr extra; }
  [StructLayout(LayoutKind.Sequential)] public struct KEY { public uint vk; public uint scan; public uint flags; public uint time; public IntPtr extra; }
  [StructLayout(LayoutKind.Sequential)] public struct MSG { public IntPtr hwnd; public uint message; public IntPtr wParam; public IntPtr lParam; public uint time; public POINT pt; }
  [DllImport("user32.dll")] private static extern IntPtr SetWindowsHookEx(int id, HookProc proc, IntPtr module, uint thread);
  [DllImport("user32.dll")] private static extern IntPtr CallNextHookEx(IntPtr hook, int code, IntPtr wParam, IntPtr lParam);
  [DllImport("user32.dll")] private static extern short GetAsyncKeyState(int key);
  [DllImport("kernel32.dll")] private static extern IntPtr GetModuleHandle(string name);
  [DllImport("user32.dll")] private static extern bool GetMessage(out MSG msg, IntPtr hwnd, uint min, uint max);
  [DllImport("user32.dll")] private static extern bool TranslateMessage(ref MSG msg);
  [DllImport("user32.dll")] private static extern IntPtr DispatchMessage(ref MSG msg);
  private static void Emit(string json) { Console.Out.WriteLine(json); Console.Out.Flush(); }
  private static string FocusedSubmission() {
    try {
      AutomationElement element = AutomationElement.FocusedElement;
      if (element == null || element.Current.IsPassword) return "";
      string control = element.Current.ControlType.ProgrammaticName.Replace("ControlType.", "");
      if (control != "Edit" && control != "Document") return "";
      // Name 往往是“请输入……”占位提示，只能读取实际 Value 或 TextPattern。
      string text = "";
      try {
        ValuePattern pattern = element.GetCurrentPattern(ValuePattern.Pattern) as ValuePattern;
        if (pattern != null) text = pattern.Current.Value ?? "";
      } catch { }
      try {
        TextPattern pattern = element.GetCurrentPattern(TextPattern.Pattern) as TextPattern;
        if (pattern != null) {
          string value = pattern.DocumentRange.GetText(-1) ?? "";
          if (value.Length > text.Length) text = value;
        }
      } catch { }
      if (text.Length > 1800) text = text.Substring(0, 1800);
      return Convert.ToBase64String(Encoding.UTF8.GetBytes(text));
    } catch { return ""; }
  }
  private static IntPtr Mouse(int code, IntPtr wParam, IntPtr lParam) {
    if (code >= 0 && ((int)wParam == 0x201 || (int)wParam == 0x204)) {
      MOUSE m = Marshal.PtrToStructure<MOUSE>(lParam);
      string button = (int)wParam == 0x201 ? "left" : "right";
      Emit("{\"type\":\"click\",\"button\":\"" + button + "\",\"x\":" + m.pt.x + ",\"y\":" + m.pt.y + "}");
    }
    return CallNextHookEx(mouseHook, code, wParam, lParam);
  }
  private static IntPtr Key(int code, IntPtr wParam, IntPtr lParam) {
    if (code >= 0 && (int)wParam == 0x100) {
      KEY k = Marshal.PtrToStructure<KEY>(lParam);
      bool ctrl = (GetAsyncKeyState(0x11) & 0x8000) != 0;
      string value = null;
      if (ctrl && k.vk == 0x53) value = "Ctrl+S";
      if (ctrl && k.vk == 0x43) value = "Ctrl+C";
      if (ctrl && k.vk == 0x56) value = "Ctrl+V";
      if (ctrl && k.vk == 0x5A) value = "Ctrl+Z";
      if (!ctrl && k.vk == 0x0D) {
        Emit("{\"type\":\"shortcut\",\"key\":\"Enter\",\"textBase64\":\"" + FocusedSubmission() + "\"}");
      } else if (value != null) {
        Emit("{\"type\":\"shortcut\",\"key\":\"" + value + "\"}");
      }
    }
    return CallNextHookEx(keyHook, code, wParam, lParam);
  }
  public static void Run() {
    mouseProc = Mouse; keyProc = Key;
    IntPtr module = GetModuleHandle(null);
    mouseHook = SetWindowsHookEx(14, mouseProc, module, 0);
    keyHook = SetWindowsHookEx(13, keyProc, module, 0);
    if (mouseHook == IntPtr.Zero && keyHook == IntPtr.Zero) { Console.Error.WriteLine("无法安装输入事件监听"); return; }
    MSG msg;
    while (GetMessage(out msg, IntPtr.Zero, 0, 0)) { TranslateMessage(ref msg); DispatchMessage(ref msg); }
  }
}
"@
[GoldenFingerInput]::Run()
`;

function parseInputEvent(line) {
  const event = JSON.parse(line);
  if (event.textBase64) event.text = Buffer.from(event.textBase64, 'base64').toString('utf8');
  delete event.textBase64;
  return event;
}

class InputMonitor {
  constructor({ onEvent, onError = () => {}, spawnImpl = spawn }) {
    this.onEvent = onEvent;
    this.onError = onError;
    this.spawn = spawnImpl;
    this.process = null;
  }

  start() {
    if (this.process || process.platform !== 'win32') return;
    this.process = this.spawn('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', WINDOWS_SCRIPT], { windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    const lines = readline.createInterface({ input: this.process.stdout });
    lines.on('line', line => {
      try { this.onEvent(parseInputEvent(line)); } catch { /* 忽略单行诊断输出 */ }
    });
    this.process.stderr.on('data', data => this.onError(String(data).trim()));
    this.process.on('error', error => this.onError(error.message));
    this.process.on('exit', () => { this.process = null; lines.close(); });
  }

  stop() {
    if (!this.process) return;
    const child = this.process;
    this.process = null;
    child.kill();
  }
}

module.exports = { InputMonitor };
