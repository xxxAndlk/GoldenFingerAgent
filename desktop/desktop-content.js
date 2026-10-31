'use strict';

const { captureWindow } = require('./window-capture');
const { readPageContent } = require('./page-content');
const { readScreenOcr, cleanLines } = require('./screen-ocr');
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
  const readUi = options.readPageContent || readPageContent;
  const readOcr = options.readScreenOcr || readScreenOcr;
  const ui = await readUi();
  if (ui.protectedContent) return { text: '', protectedContent: true, evidencePath: '', source: 'protected' };
  const page = { activeTab: ui.activeTab || '', url: ui.url || '' };
  let evidencePath = '';
  let ocr = { text: '', lines: [] };
  try {
    evidencePath = await withTimeout(
      capture({ ...options, pageRegion: ui.contentRect || ui.documentRect, windowRect: ui.windowRect }),
      6_000,
      '截图超时',
    );
    if (options.windowKey) {
      const compare = options.hasMeaningfulVisualChange || hasMeaningfulVisualChange;
      const visual = await withTimeout(compare(options.windowKey, evidencePath), 3_000, '画面比较超时');
      if (!visual.changed) {
        if (evidencePath) fs.rmSync(evidencePath, { force: true });
        return { ...page, text: '', protectedContent: false, evidencePath: '', source: 'unchanged', unchanged: true };
      }
    }
    ocr = await withTimeout(readOcr(evidencePath), 8_000, 'OCR 超时');
  } catch { /* 截图、画面比较或 OCR 不可用时退回界面文字。 */ }
  const text = ocr.text
    ? mergeLines(ocr.text, ui.focusedText || '')
    : mergeLines(ui.text || '');
  return { ...page, text, protectedContent: false, evidencePath, source: ocr.text ? 'ocr' : 'uia' };
}

module.exports = { readDesktopContent, mergeLines };
