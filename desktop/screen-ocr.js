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
    [PSCustomObject]@{ lines = @($result.Lines | ForEach-Object { $_.Text }) } | ConvertTo-Json -Compress -Depth 3
  } finally { if ($bitmap) { $bitmap.Dispose() } }
} finally { if ($stream) { $stream.Dispose() } }
`;

function cleanLines(lines) {
  const values = Array.isArray(lines) ? lines : lines ? [lines] : [];
  return [...new Set(values.map(line => String(line || '').replace(/\s+/g, ' ').trim().slice(0, 1000)).filter(line => line.length >= 2))];
}

function parseOcrOutput(stdout) {
  const raw = String(stdout || '').trim();
  if (!raw) return { text: '', lines: [] };
  const lines = cleanLines(JSON.parse(raw).lines);
  return { text: lines.join('\n').slice(0, 8000), lines };
}

function readScreenOcr(imagePath, execFileImpl = execFile) {
  if (process.platform !== 'win32' || !imagePath) return Promise.resolve({ text: '', lines: [] });
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
        return resolve({ text: '', lines: [], error: detail.slice(-500) });
      }
      try { resolve(parseOcrOutput(stdout)); } catch (parseError) { resolve({ text: '', lines: [], error: parseError.message }); }
    });
  });
}

module.exports = { readScreenOcr, parseOcrOutput, cleanLines };
