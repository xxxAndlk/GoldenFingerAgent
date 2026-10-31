'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { parsePageContent } = require('../page-content');

test('page content preserves visible paragraphs and removes duplicates', () => {
  const result = parsePageContent(JSON.stringify({ lines: ['项目方案', '记录页面正文，而不是点击坐标。', '项目方案'] }));
  assert.equal(result.text, '项目方案\n记录页面正文，而不是点击坐标。');
  assert.equal(result.protectedContent, false);
});

test('page content never returns password field text', () => {
  const result = parsePageContent(JSON.stringify({ protectedContent: true, lines: ['不应出现'] }));
  assert.deepEqual(result, { text: '', protectedContent: true });
});

test('page content parses the selected tab, URL and current document region', () => {
  const result = parsePageContent(JSON.stringify({
    lines: ['当前页面正文'], activeTab: '项目设计', url: 'https://example.com/design?q=1',
    documentRect: { x: 10, y: 120, width: 1180, height: 650 },
    windowRect: { x: 0, y: 0, width: 1200, height: 800 },
  }));
  assert.equal(result.activeTab, '项目设计');
  assert.equal(result.url, 'https://example.com/design?q=1');
  assert.deepEqual(result.documentRect, { x: 10, y: 120, width: 1180, height: 650 });
  assert.equal(result.windowRect.width, 1200);
});

test('page content safely falls back when tab metadata is unavailable or invalid', () => {
  const result = parsePageContent(JSON.stringify({ lines: ['普通窗口正文'], url: 'not-a-url', documentRect: { width: -1 } }));
  assert.equal(result.text, '普通窗口正文');
  assert.equal(result.activeTab, '');
  assert.equal(result.url, '');
  assert.equal(result.documentRect, null);
  assert.equal(result.windowRect, null);
});

test('main landmark keeps page content and excludes the internal sidebar', () => {
  const result = parsePageContent(JSON.stringify({
    documentRect: { x: 0, y: 100, width: 1000, height: 700 },
    mainRect: { x: 280, y: 100, width: 720, height: 700 },
    entries: [
      { text: '其他会话和导航', rect: { x: 20, y: 200, width: 180, height: 30 } },
      { text: '当前页面的主要正文', rect: { x: 300, y: 200, width: 600, height: 60 } },
    ],
  }));
  assert.equal(result.text, '当前页面的主要正文');
  assert.equal(result.contentStrategy, 'landmark');
  assert.deepEqual(result.contentRect, { x: 280, y: 100, width: 720, height: 700 });
});

test('navigation landmark is removed even without a main landmark', () => {
  const result = parsePageContent(JSON.stringify({
    documentRect: { x: 0, y: 100, width: 1000, height: 700 },
    navigationRects: [{ x: 0, y: 100, width: 260, height: 700 }],
    entries: [
      { text: '左侧会话列表', rect: { x: 20, y: 200, width: 180, height: 30 } },
      { text: '当前页面正文', rect: { x: 300, y: 200, width: 500, height: 60 } },
    ],
  }));
  assert.equal(result.text, '当前页面正文');
  assert.equal(result.contentStrategy, 'navigation');
  assert.equal(result.contentRect.x, 268);
});

test('text layout filters a sidebar when the page has no semantic landmarks', () => {
  const entries = [150, 220, 290, 360, 430].map((y, index) => ({ text: `历史会话 ${index}`, rect: { x: 20, y, width: 180, height: 30 } }));
  entries.push(
    { text: '这是当前会话的重要正文内容', rect: { x: 300, y: 180, width: 600, height: 60 } },
    { text: '这是当前会话后续的实施计划', rect: { x: 300, y: 320, width: 600, height: 60 } },
  );
  const result = parsePageContent(JSON.stringify({ documentRect: { x: 0, y: 100, width: 1000, height: 700 }, entries }));
  assert.equal(result.contentStrategy, 'layout');
  assert.equal(result.contentRect.x, 288);
  assert.doesNotMatch(result.text, /历史会话/);
  assert.match(result.text, /当前会话/);
});
