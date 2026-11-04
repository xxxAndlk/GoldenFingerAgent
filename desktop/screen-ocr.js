'use strict';

const { execFile } = require('node:child_process');

// 使用 Windows 自带 OCR 在本机读取窗口截图，适用于浏览器、Electron、WPS 等桌面软件。
const WINDOWS_SCRIPT = String.raw`
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Runtime.WindowsRuntime
[Windows.Storage.Streams.InMemoryRandomAccessStream, Windows.Storage.Streams, ContentType=WindowsRuntime] | Out-Null
[Windows.Storage.Streams.DataWriter, Windows.Storage.Streams, ContentType=WindowsRuntime] | Out-Null
[Windows.Graphics.Imaging.BitmapDecoder, Windows.Graphics.Imaging, ContentType=WindowsRuntime] | Out-Null
[Windows.Graphics.Imaging.SoftwareBitmap, Windows.Graphics.Imaging, ContentType=WindowsRuntime] | Out-Null
[Windows.Media.Ocr.OcrEngine, Windows.Media.Ocr, ContentType=WindowsRuntime] | Out-Null
[Windows.Media.Ocr.OcrResult, Windows.Media.Ocr, ContentType=WindowsRuntime] | Out-Null
$asTaskGeneric = [System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object {
  $_.Name -eq 'AsTask' -and $_.IsGenericMethod -and $_.GetParameters().Count -eq 1
} | Select-Object -First 1
function Await-WinRT($operation, $resultType) {
  $task = $asTaskGeneric.MakeGenericMethod($resultType).Invoke($null, @($operation))
  $task.Wait()
  return $task.Result
}
$chinese = [Windows.Media.Ocr.OcrEngine]::AvailableRecognizerLanguages | Where-Object { $_.LanguageTag -like 'zh-*' } | Select-Object -First 1
$engine = $(if ($chinese) { [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage($chinese) } else { [Windows.Media.Ocr.OcrEngine]::TryCreateFromUserProfileLanguages() })
if (-not $engine) { [PSCustomObject]@{ lines = @() } | ConvertTo-Json -Compress; exit 0 }
# 低完整性/AppContainer 下 StorageFile 的文件代理会返回空流；
# 直接用内存流交给 BitmapDecoder，不经过文件代理。
$bytes = [System.IO.File]::ReadAllBytes('__IMAGE_PATH__')
$stream = New-Object Windows.Storage.Streams.InMemoryRandomAccessStream
$writer = New-Object Windows.Storage.Streams.DataWriter([Windows.Storage.Streams.IOutputStream]$stream)
$writer.WriteBytes($bytes)
Await-WinRT ($writer.StoreAsync()) ([uint32]) | Out-Null
$writer.DetachStream() | Out-Null
$stream.Seek(0)
try {
  $decoder = Await-WinRT ([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($stream)) ([Windows.Graphics.Imaging.BitmapDecoder])
  $bitmap = Await-WinRT ($decoder.GetSoftwareBitmapAsync()) ([Windows.Graphics.Imaging.SoftwareBitmap])
  try {
    $result = Await-WinRT ($engine.RecognizeAsync($bitmap)) ([Windows.Media.Ocr.OcrResult])
    $lineObjects = @($result.Lines | ForEach-Object {
      $words = @($_.Words)
      if ($words.Count -eq 0) { return }
      $rects = @($words | ForEach-Object { $_.BoundingRect })
      $left = ($rects | Measure-Object -Property X -Minimum).Minimum
      $top = ($rects | Measure-Object -Property Y -Minimum).Minimum
      $right = ($rects | ForEach-Object { $_.X + $_.Width } | Measure-Object -Maximum).Maximum
      $bottom = ($rects | ForEach-Object { $_.Y + $_.Height } | Measure-Object -Maximum).Maximum
      [PSCustomObject]@{
        text = $_.Text
        x = [math]::Round($left, 2); y = [math]::Round($top, 2)
        width = [math]::Round($right - $left, 2); height = [math]::Round($bottom - $top, 2)
      }
    })
    [PSCustomObject]@{
      lines = @($lineObjects | ForEach-Object { $_.text })
      lineBoxes = $lineObjects
    } | ConvertTo-Json -Compress -Depth 5
  } finally { if ($bitmap) { $bitmap.Dispose() } }
} finally { if ($stream) { $stream.Dispose() } }
`;

function cleanLines(lines) {
  const values = Array.isArray(lines) ? lines : lines ? [lines] : [];
  return [...new Set(values.map(line => String(line || '').replace(/\s+/g, ' ').trim().slice(0, 1000)).filter(line => line.length >= 2))];
}

function parseOcrOutput(stdout) {
  const raw = String(stdout || '').trim();
  if (!raw) return { text: '', lines: [], boxes: [] };
  const value = JSON.parse(raw);
  const lines = cleanLines(value.lines);
  const boxes = (Array.isArray(value.lineBoxes) ? value.lineBoxes : []).map(box => {
    const text = String((box && box.text) || '').replace(/\s+/g, ' ').trim();
    const numbers = ['x', 'y', 'width', 'height'].map(key => Number(box && box[key]));
    if (!text || numbers.some(number => !Number.isFinite(number) || Math.abs(number) > 100000)) return null;
    return { text, x: numbers[0], y: numbers[1], width: numbers[2], height: numbers[3] };
  }).filter(Boolean);
  return { text: lines.join('\n').slice(0, 8000), lines, boxes };
}

function readScreenOcr(imagePath, execFileImpl = execFile) {
  if (process.platform !== 'win32' || !imagePath) return Promise.resolve({ text: '', lines: [], boxes: [] });
  const script = WINDOWS_SCRIPT.replace('__IMAGE_PATH__', require('node:path').resolve(imagePath).replace(/'/g, "''"));
  return new Promise(resolve => {
    execFileImpl('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', script], {
      timeout: 16_000,
      windowsHide: true,
      encoding: 'utf8',
      maxBuffer: 128 * 1024,
    }, (error, stdout, stderr) => {
      if (error) {
        const detail = String(stderr || '').trim() || String(error.message || '').trim();
        return resolve({ text: '', lines: [], boxes: [], error: detail.slice(-500) });
      }
      try { resolve(parseOcrOutput(stdout)); } catch (parseError) { resolve({ text: '', lines: [], boxes: [], error: parseError.message }); }
    });
  });
}

module.exports = { readScreenOcr, parseOcrOutput, cleanLines };
