'use strict';

const { execFile } = require('node:child_process');

// 只读当前前台窗口的可访问性树，不注入目标应用，也不读取密码字段。
const WINDOWS_SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
try {
  Add-Type -AssemblyName UIAutomationClient -ErrorAction Stop
  Add-Type -AssemblyName UIAutomationTypes -ErrorAction Stop
  Add-Type -AssemblyName WindowsBase -ErrorAction SilentlyContinue
} catch { exit 0 }
function New-Rectangle($bounds) {
  return [PSCustomObject]@{
    x = [Math]::Round($bounds.X, 2); y = [Math]::Round($bounds.Y, 2)
    width = [Math]::Round($bounds.Width, 2); height = [Math]::Round($bounds.Height, 2)
  }
}
# 用 UI Automation 的焦点元素向上找顶层窗口，避免 P/Invoke 编译；
# 低完整性进程（例如 --no-sandbox 启动）不允许 Add-Type C#。
$focused = [System.Windows.Automation.AutomationElement]::FocusedElement
if (-not $focused) { exit 0 }
$walker = [System.Windows.Automation.TreeWalker]::ControlViewWalker
$root = $focused
while ($root -and $root.Current.ControlType.ProgrammaticName -ne 'ControlType.Window') {
  $root = $walker.GetParent($root)
}
if (-not $root) { $root = $focused }
$windowBounds = $root.Current.BoundingRectangle
$windowRect = New-Rectangle $windowBounds
if ($focused -and $focused.Current.IsPassword) {
  [PSCustomObject]@{ protectedContent = $true; lines = @() } | ConvertTo-Json -Compress
  exit 0
}
$focusedText = ''
if ($focused -and -not $focused.Current.IsOffscreen) {
  try {
    $focusedText = $focused.Current.Name
    $focusedControl = $focused.Current.ControlType.ProgrammaticName.Replace('ControlType.', '')
    if ($focusedControl -eq 'Edit' -or $focusedControl -eq 'Document') {
      $focusedPattern = $focused.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern)
      if ($focusedPattern -and $focusedPattern.Current.Value.Length -gt $focusedText.Length) { $focusedText = $focusedPattern.Current.Value }
    }
    $focusedText = ($focusedText -replace '\s+', ' ').Trim()
  } catch { $focusedText = '' }
}
$queue = New-Object System.Collections.Generic.Queue[System.Windows.Automation.AutomationElement]
$queue.Enqueue($root)
$entries = New-Object System.Collections.Generic.List[object]
$visited = 0
$length = 0
$activeTab = ''
$url = ''
$documentBounds = [System.Windows.Rect]::Empty
$documentArea = 0
$mainBounds = [System.Windows.Rect]::Empty
$mainArea = 0
$navigationRects = New-Object System.Collections.Generic.List[object]
$allowed = @('Text', 'Document', 'Edit', 'DataItem', 'ListItem', 'Hyperlink')
while ($queue.Count -gt 0 -and $visited -lt 500) {
  $element = $queue.Dequeue()
  $visited += 1
  try {
    $current = $element.Current
    if (-not $current.IsOffscreen -and -not $current.IsPassword) {
      $control = $current.ControlType.ProgrammaticName.Replace('ControlType.', '')
      $bounds = $current.BoundingRectangle
      if ($control -eq 'TabItem' -and -not $activeTab) {
        try {
          $selection = $element.GetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern)
          if ($selection -and $selection.Current.IsSelected) { $activeTab = ($current.Name -replace '\s+', ' ').Trim() }
        } catch { }
      }
      if ($control -eq 'Document' -and $bounds.Width -gt 0 -and $bounds.Height -gt 0) {
        $area = $bounds.Width * $bounds.Height
        if ($area -gt $documentArea) { $documentBounds = $bounds; $documentArea = $area }
      }
      if ($bounds.Width -gt 0 -and $bounds.Height -gt 0) {
        try {
          $landmark = $element.GetCurrentPropertyValue([System.Windows.Automation.AutomationElement]::LandmarkTypeProperty).ToString()
          if ($landmark -eq 'Main') {
            $area = $bounds.Width * $bounds.Height
            if ($area -gt $mainArea) { $mainBounds = $bounds; $mainArea = $area }
          } elseif ($landmark -eq 'Navigation') {
            $navigationRects.Add((New-Rectangle $bounds))
          }
        } catch { }
      }
      if ($allowed -contains $control -and $length -lt 8000) {
        $text = $current.Name
        $value = ''
        if ($control -eq 'Edit' -or $control -eq 'Document') {
          try {
            $pattern = $element.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern)
            if ($pattern) {
              $value = $pattern.Current.Value
              if ($value.Length -gt $text.Length) { $text = $value }
            }
          } catch { }
        }
        if ($control -eq 'Edit' -and -not $url -and $value -match '^https?://') { $url = $value.Trim() }
        $text = ($text -replace '\s+', ' ').Trim()
        if ($text.Length -ge 2) {
          $entries.Add([PSCustomObject]@{ text = $text; rect = (New-Rectangle $bounds) })
          $length += $text.Length
        }
      }
    }
    $children = $element.FindAll([System.Windows.Automation.TreeScope]::Children, [System.Windows.Automation.Condition]::TrueCondition)
    foreach ($child in $children) { $queue.Enqueue($child) }
  } catch { }
}
$lines = New-Object System.Collections.Generic.List[string]
foreach ($entry in $entries) {
  if (-not $documentBounds.IsEmpty) {
    $rect = $entry.rect
    if ($rect.width -le 0 -or $rect.height -le 0) { continue }
    $centerX = $rect.x + $rect.width / 2
    $centerY = $rect.y + $rect.height / 2
    if ($centerX -lt $documentBounds.Left -or $centerX -gt $documentBounds.Right -or $centerY -lt $documentBounds.Top -or $centerY -gt $documentBounds.Bottom) { continue }
  }
  $lines.Add($entry.text)
}
$documentRect = if ($documentBounds.IsEmpty) { $null } else { New-Rectangle $documentBounds }
$mainRect = if ($mainBounds.IsEmpty) { $null } else { New-Rectangle $mainBounds }
[PSCustomObject]@{
  protectedContent = $false; lines = $lines; entries = $entries; activeTab = $activeTab; url = $url
  focusedText = $focusedText; documentRect = $documentRect; mainRect = $mainRect
  navigationRects = $navigationRects; windowRect = $windowRect
} | ConvertTo-Json -Compress -Depth 4
`;

function cleanText(value, length) {
  return String(value || '').replace(/\s+/g, ' ').trim().slice(0, length);
}

function parseRectangle(value) {
  if (!value || typeof value !== 'object') return null;
  const rect = {};
  for (const key of ['x', 'y', 'width', 'height']) {
    const number = Number(value[key]);
    if (!Number.isFinite(number) || Math.abs(number) > 100000) return null;
    rect[key] = number;
  }
  return rect.width > 0 && rect.height > 0 ? rect : null;
}

function centerInside(rect, container) {
  const x = rect.x + rect.width / 2;
  const y = rect.y + rect.height / 2;
  return x >= container.x && x <= container.x + container.width && y >= container.y && y <= container.y + container.height;
}

function clipRectangle(rect, container) {
  if (!rect) return null;
  if (!container) return rect;
  const left = Math.max(rect.x, container.x);
  const top = Math.max(rect.y, container.y);
  const right = Math.min(rect.x + rect.width, container.x + container.width);
  const bottom = Math.min(rect.y + rect.height, container.y + container.height);
  return right - left > 0 && bottom - top > 0 ? { x: left, y: top, width: right - left, height: bottom - top } : null;
}

function usableContentRegion(rect, documentRect) {
  if (!rect || rect.width < 120 || rect.height < 80) return false;
  return !documentRect || (rect.width * rect.height >= documentRect.width * documentRect.height * 0.08 && rect.width >= documentRect.width * 0.35);
}

function inferContentRegion(entries, documentRect) {
  if (!documentRect) return { rect: null, strategy: 'window' };
  const inside = entries.filter(entry => centerInside(entry.rect, documentRect));
  const wide = inside.filter(entry => entry.text.length >= 8 && entry.rect.width >= documentRect.width * 0.18 && entry.rect.height <= documentRect.height * 0.25);
  if (wide.length >= 2) {
    const mainLeft = Math.min(...wide.map(entry => entry.rect.x));
    const relativeLeft = (mainLeft - documentRect.x) / documentRect.width;
    const leftEntries = inside.filter(entry => entry.rect.x + entry.rect.width <= mainLeft + 4);
    if (relativeLeft > 0.1 && relativeLeft < 0.45 && leftEntries.length >= 4) {
      const top = Math.min(...leftEntries.map(entry => entry.rect.y));
      const bottom = Math.max(...leftEntries.map(entry => entry.rect.y + entry.rect.height));
      if (bottom - top >= documentRect.height * 0.3) {
        const rect = { x: Math.max(documentRect.x, mainLeft - 12), y: documentRect.y, width: documentRect.x + documentRect.width - Math.max(documentRect.x, mainLeft - 12), height: documentRect.height };
        if (usableContentRegion(rect, documentRect)) return { rect, strategy: 'layout' };
      }
    }
  }
  return { rect: documentRect, strategy: 'document' };
}

function selectPrimaryContent(entries, documentRect, mainRect, navigationRects = []) {
  const semantic = clipRectangle(mainRect, documentRect);
  let selected;
  if (usableContentRegion(semantic, documentRect)) {
    selected = { rect: semantic, strategy: 'landmark' };
  } else if (documentRect) {
    const leftNavigation = navigationRects.filter(rect => {
      if (!rect || !centerInside(rect, documentRect)) return false;
      return rect.x <= documentRect.x + documentRect.width * 0.06 && rect.width >= 40
        && rect.width <= documentRect.width * 0.45 && rect.height >= documentRect.height * 0.3;
    }).sort((a, b) => (b.x + b.width) - (a.x + a.width))[0];
    if (leftNavigation) {
      const left = leftNavigation.x + leftNavigation.width + 8;
      const rect = { x: left, y: documentRect.y, width: documentRect.x + documentRect.width - left, height: documentRect.height };
      selected = usableContentRegion(rect, documentRect) ? { rect, strategy: 'navigation' } : inferContentRegion(entries, documentRect);
    } else selected = inferContentRegion(entries, documentRect);
  } else selected = inferContentRegion(entries, documentRect);
  return { ...selected, entries: selected.rect ? entries.filter(entry => centerInside(entry.rect, selected.rect)) : entries };
}

function parsePageContent(stdout) {
  const text = String(stdout || '').trim();
  if (!text) return { text: '', protectedContent: false, activeTab: '', url: '', focusedText: '', documentRect: null, contentRect: null, windowRect: null };
  const value = JSON.parse(text);
  if (value.protectedContent) return { text: '', protectedContent: true };
  const lines = Array.isArray(value.lines) ? value.lines : value.lines ? [value.lines] : [];
  const rawEntries = Array.isArray(value.entries) ? value.entries : [];
  const entries = rawEntries.map(entry => ({ text: cleanText(entry && entry.text, 1000), rect: parseRectangle(entry && entry.rect) })).filter(entry => entry.text.length >= 2 && entry.rect);
  const documentRect = parseRectangle(value.documentRect);
  const mainRect = parseRectangle(value.mainRect);
  const rawNavigation = Array.isArray(value.navigationRects) ? value.navigationRects : [];
  const navigationRects = rawNavigation.map(parseRectangle).filter(Boolean);
  const primary = selectPrimaryContent(entries, documentRect, mainRect, navigationRects);
  const sourceLines = rawEntries.length ? primary.entries.map(entry => entry.text) : lines;
  const unique = [...new Set(sourceLines.map(line => cleanText(line, 1000)).filter(line => line.length >= 2))];
  const url = cleanText(value.url, 2000);
  return {
    text: unique.join('\n').slice(0, 6000),
    protectedContent: false,
    activeTab: cleanText(value.activeTab, 300),
    url: /^https?:\/\//i.test(url) ? url : '',
    focusedText: cleanText(value.focusedText, 2000),
    documentRect,
    contentRect: primary.rect,
    contentStrategy: primary.strategy,
    windowRect: parseRectangle(value.windowRect),
  };
}

function readPageContent(execFileImpl = execFile) {
  if (process.platform !== 'win32') return Promise.resolve({ text: '', protectedContent: false });
  return new Promise(resolve => {
    execFileImpl('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', WINDOWS_SCRIPT], {
      timeout: 4000,
      windowsHide: true,
      encoding: 'utf8',
      maxBuffer: 256 * 1024,
    }, (error, stdout) => {
      if (error) return resolve({ text: '', protectedContent: false });
      try { resolve(parsePageContent(stdout)); } catch { resolve({ text: '', protectedContent: false }); }
    });
  });
}

module.exports = { readPageContent, parsePageContent, selectPrimaryContent };
