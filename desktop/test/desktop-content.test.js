'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { readDesktopContent, mergeLines } = require('../desktop-content');

test('generic desktop content uses OCR for any application and merges focused text', async () => {
  const result = await readDesktopContent({
    captureWindow: async () => 'capture.jpg',
    readPageContent: async () => ({ text: '额度与导航', focusedText: '这是当前输入的问题', protectedContent: false }),
    readScreenOcr: async () => ({ text: '第一段工作正文\n第二段工作正文', lines: [] }),
  });
  assert.equal(result.source, 'ocr');
  assert.match(result.text, /第一段工作正文/);
  assert.match(result.text, /当前输入的问题/);
  assert.doesNotMatch(result.text, /额度与导航/);
});

test('generic desktop content falls back to accessibility text without OCR', async () => {
  const result = await readDesktopContent({
    captureWindow: async () => { throw new Error('protected window'); },
    readPageContent: async () => ({ text: 'WPS 文档正文', protectedContent: false }),
    readScreenOcr: async () => ({ text: '', lines: [] }),
  });
  assert.equal(result.source, 'uia');
  assert.equal(result.text, 'WPS 文档正文');
});

test('mergeLines removes repeated text across sources', () => {
  assert.equal(mergeLines('同一段正文', '同一段正文\n新的正文'), '同一段正文\n新的正文');
});

test('unchanged desktop frames skip OCR and produce no evidence', async () => {
  let ocrRuns = 0;
  const result = await readDesktopContent({
    windowKey: 'WorkBuddy\0Chat',
    captureWindow: async () => 'frame.png',
    readPageContent: async () => ({ text: '固定文字', protectedContent: false }),
    hasMeaningfulVisualChange: async () => ({ changed: false }),
    readScreenOcr: async () => { ocrRuns += 1; return { text: '不应读取', lines: [] }; },
  });
  assert.equal(result.unchanged, true);
  assert.equal(result.text, '');
  assert.equal(ocrRuns, 0);
});

test('desktop content falls back to screen region capture when the window thumbnail is empty', async () => {
  const contentRect = { x: 20, y: 30, width: 900, height: 700 };
  const windowRect = { x: 0, y: 0, width: 1000, height: 800 };
  let screenOptions;
  const result = await readDesktopContent({
    title: '项目设计 - Google Chrome',
    captureWindow: async () => { throw new Error('窗口当前不可见或不允许截图'); },
    captureScreenRegion: async options => { screenOptions = options; return 'screen.png'; },
    readPageContent: async () => ({ text: '', contentRect, windowRect, protectedContent: false }),
    readScreenOcr: async () => ({ text: '屏幕区域读到的正文', lines: [] }),
  });
  assert.equal(screenOptions.pageRegion, contentRect);
  assert.equal(screenOptions.windowRect, windowRect);
  assert.equal(result.source, 'ocr');
  assert.match(result.text, /屏幕区域读到的正文/);
});

test('desktop content redacts secrets and drops sensitive evidence', async () => {
  const result = await readDesktopContent({
    title: 'settings.json - VS Code',
    captureWindow: async () => 'secret-shot.png',
    readPageContent: async () => ({ text: '', protectedContent: false }),
    readScreenOcr: async () => ({ text: 'api_key\nsk 一 d64b36 92s894C7s8e21b4b952d67d43', lines: [] }),
  });
  assert.equal(result.sensitive, true);
  assert.equal(result.evidencePath, '');
  assert.equal(result.skipEvidence, true);
  assert.doesNotMatch(result.text, /d64b36|92s894/);
});

test('desktop content passes page regions to capture and returns active tab metadata', async () => {
  let captureOptions;
  const documentRect = { x: 0, y: 100, width: 1200, height: 700 };
  const contentRect = { x: 250, y: 100, width: 950, height: 700 };
  const windowRect = { x: 0, y: 0, width: 1200, height: 800 };
  const result = await readDesktopContent({
    title: '项目设计 - Google Chrome',
    captureWindow: async options => { captureOptions = options; return 'page.png'; },
    readPageContent: async () => ({ text: '正文', activeTab: '项目设计', url: 'https://example.com/design', documentRect, contentRect, windowRect }),
    readScreenOcr: async () => ({ text: '只有当前页面的正文', lines: [] }),
  });
  assert.equal(captureOptions.pageRegion, contentRect);
  assert.equal(captureOptions.windowRect, windowRect);
  assert.equal(result.activeTab, '项目设计');
  assert.equal(result.url, 'https://example.com/design');
  assert.equal(result.text, '只有当前页面的正文');
});
