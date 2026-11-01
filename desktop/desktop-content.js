'use strict';

const { captureWindow } = require('./window-capture');
const { captureScreenRegion } = require('./screen-capture');
const { readPageContent } = require('./page-content');
const { readScreenOcr, cleanLines } = require('./screen-ocr');
const { containsSensitiveContent, redactSensitiveContent } = require('./redaction');
const { hasMeaningfulVisualChange } = require('./visual-change');
const fs = require('node:fs');

function withTimeout(promise, ms, label) {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(label || '操作超时')), ms);
    }),
  ]).finally(() => clearTimeout(timer));
}

function mergeLines(...groups) {
  const seen = new Set();
  const lines = [];
  for (const group of groups) {
    for (const line of cleanLines(String(group || '').split('\n'))) {
      const key = line.replace(/\s+/g, '').toLowerCase();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      lines.push(line);
    }
  }
  return lines.join('\n').slice(0, 8000);
}

async function readDesktopContent(options) {
  const capture = options.captureWindow || captureWindow;
  const captureScreen = options.captureScreenRegion || captureScreenRegion;
  const readUi = options.readPageContent || readPageContent;
  const readOcr = options.readScreenOcr || readScreenOcr;
  const ui = await readUi();
  if (ui.protectedContent) return { text: '', protectedContent: true, evidencePath: '', source: 'protected' };
  const page = { activeTab: ui.activeTab || '', url: ui.url || '' };
  let evidencePath = '';
  let ocr = { text: '', lines: [] };
  let screenFallback = false;
  const pageRegion = ui.contentRect || ui.documentRect;
  try {
    evidencePath = await withTimeout(
      capture({ ...options, pageRegion, windowRect: ui.windowRect }),
      6_000,
      '截图超时',
    );
    if (!evidencePath) throw new Error('窗口缩略图为空');
  } catch {
    // Electron 的窗口缩略图在低完整性进程/提权窗口上可能为空；
    // 改为抓当前屏幕的窗口区域，仍由本机 OCR 读取，不依赖目标应用配合。
    screenFallback = true;
    try {
      evidencePath = await withTimeout(
        captureScreen({ ...options, pageRegion, windowRect: ui.windowRect }),
        16_000,
        '区域截图超时',
      );
    } catch { /* 两种截图都不可用时退回界面文字。 */ }
  }
  try {
    if (evidencePath && options.windowKey) {
      const compare = options.hasMeaningfulVisualChange || hasMeaningfulVisualChange;
      const visual = await withTimeout(compare(options.windowKey, evidencePath), 3_000, '画面比较超时');
      if (!visual.changed) {
        if (evidencePath) fs.rmSync(evidencePath, { force: true });
        return { ...page, text: '', protectedContent: false, evidencePath: '', source: 'unchanged', unchanged: true };
      }
    }
    if (evidencePath) ocr = await withTimeout(readOcr(evidencePath), 15_000, 'OCR 超时');
  } catch { /* 截图、画面比较或 OCR 不可用时退回界面文字。 */ }
  const rawText = ocr.text
    ? mergeLines(ocr.text, ui.focusedText || '')
    : mergeLines(ui.text || '');
  const redacted = redactSensitiveContent(rawText);
  const sensitive = redacted.redacted || containsSensitiveContent(rawText);
  let captureNote = '';
  if (evidencePath && sensitive) {
    fs.rmSync(evidencePath, { force: true });
    evidencePath = '';
    captureNote = '截图含敏感内容，已丢弃';
  } else if (screenFallback && evidencePath && !ocr.text) {
    // 区域截图没有读到文字时，无法确认画面是否含密钥，不保留证据。
    let size = 0;
    try { size = fs.statSync(evidencePath).size; } catch { /* ignore */ }
    fs.rmSync(evidencePath, { force: true });
    evidencePath = '';
    captureNote = `未读取到文字（${Math.round(size / 1024)}KB${ocr.error ? `，${cleanLines([ocr.error])[0] || 'OCR 错误'}` : ''}），已丢弃区域截图`;
  }
  return {
    ...page,
    text: redacted.text,
    protectedContent: false,
    evidencePath,
    source: ocr.text ? 'ocr' : 'uia',
    sensitive,
    skipEvidence: Boolean(captureNote),
    captureNote,
  };
}

module.exports = { readDesktopContent, mergeLines };
