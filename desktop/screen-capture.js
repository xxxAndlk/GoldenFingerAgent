'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { execFile } = require('node:child_process');

// desktopCapturer 在低完整性 / 提权窗口上经常返回空缩略图；
// 用屏幕区域截图兜底，先抓到像素，再交给 Windows OCR 读正文。
const WINDOWS_SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Drawing -ErrorAction Stop
$left = [Math]::Max(0, [int]__X__)
$top = [Math]::Max(0, [int]__Y__)
$right = [int]__X__ + [int]__W__
$bottom = [int]__Y__ + [int]__H__
$width = $right - $left
$height = $bottom - $top
if ($width -lt 32 -or $height -lt 32) { exit 2 }
$bitmap = New-Object System.Drawing.Bitmap $width, $height
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
try {
  $graphics.CopyFromScreen($left, $top, 0, 0, $bitmap.Size)
  $bitmap.Save('__TARGET__', [System.Drawing.Imaging.ImageFormat]::Png)
} finally {
  $graphics.Dispose()
  $bitmap.Dispose()
}
`;

function validRect(rect) {
  return rect
    && ['x', 'y', 'width', 'height'].every(key => Number.isFinite(Number(rect[key])))
    && Number(rect.width) >= 32
    && Number(rect.height) >= 32;
}

function captureScreenRegion({
  windowRect,
  pageRegion,
  outputDir,
  at = Date.now(),
  execFileImpl = execFile,
} = {}) {
  const rect = validRect(pageRegion) ? pageRegion : windowRect;
  if (!validRect(rect) || !outputDir) return Promise.reject(new Error('没有可用的窗口区域'));
  const target = path.join(
    path.resolve(outputDir),
    `${new Date(at).toISOString().replace(/[:.]/g, '-')}-${Math.random().toString(36).slice(2, 8)}.png`,
  );
  fs.mkdirSync(path.dirname(target), { recursive: true });
  const script = WINDOWS_SCRIPT
    .replaceAll('__X__', String(Math.round(Number(rect.x))))
    .replaceAll('__Y__', String(Math.round(Number(rect.y))))
    .replaceAll('__W__', String(Math.round(Number(rect.width))))
    .replaceAll('__H__', String(Math.round(Number(rect.height))))
    .replaceAll('__TARGET__', target.replace(/'/g, "''"));
  return new Promise((resolve, reject) => {
    execFileImpl('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', script], {
      timeout: 20_000,
      windowsHide: true,
      encoding: 'utf8',
      maxBuffer: 32 * 1024,
    }, error => {
      if (error) {
        try { fs.rmSync(target, { force: true }); } catch { /* nothing to clean */ }
        reject(new Error(`区域截图失败：${error.message}`));
        return;
      }
      resolve(target);
    });
  });
}

module.exports = { captureScreenRegion };
