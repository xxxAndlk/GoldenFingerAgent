'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function read(name) {
  return fs.readFileSync(path.join(__dirname, '..', name), 'utf8');
}

test('聊天窗口提供清屏按钮，并按“只隐藏、可向上滚动恢复”实现', () => {
  const html = read('index.html');
  const renderer = read('renderer.js');
  const styles = read('styles.css');

  assert.match(html, /id="clear-screen"/);
  assert.match(html, /id="clear-hint"/);
  assert.match(html, /清屏/);
  assert.match(styles, /\.messages \.cleared-hidden/);

  // 清屏只隐藏节点，不删除服务端内容。
  assert.match(renderer, /function clearScreen\(\)/);
  assert.match(renderer, /classList\.add\('cleared-hidden'\)/);
  assert.doesNotMatch(renderer, /function clearScreen\(\)[\s\S]{0,900}?api\.(delete|clear|remove)/);
  // 用时间点记录清屏范围，中途重绘也不会把清掉的内容放回来。
  assert.match(renderer, /clearedCutoff = Date\.now\(\)/);
  assert.match(renderer, /function isClearedItem\(item\)/);
  // 清屏后不留等高空位，新消息直接贴顶，不会悬在空白中间。
  assert.doesNotMatch(renderer, /clear-spacer/);
  // 恢复：还在清屏时向上滚动（没有滚动条时用滚轮手势）才恢复。
  assert.match(renderer, /function restoreCleared\(\)/);
  assert.match(renderer, /if \(clearedActive\) \{[\s\S]{0,200}?restoreCleared\(\);/);
  assert.match(renderer, /addEventListener\('wheel'/);
  assert.match(renderer, /classList\.remove\('cleared-hidden'\)/);
  assert.match(renderer, /clearSettleUntil/);
});

test('重新打开窗口只保留最近一页并落在最底部', () => {
  const renderer = read('renderer.js');
  assert.match(renderer, /const MESSAGE_PAGE_SIZE = 50;/);
  assert.match(renderer, /function scrollToBottom\(\)/);
  // 打开面板时重新拉最近一页，并且用瞬时定位避免 smooth 动画停在中间。
  assert.match(renderer, /if \(next\) \{[\s\S]{0,240}?resetCleared\(\);[\s\S]{0,120}?reloadLatestMessages\(\);/);
  // 消息统一从会话投影读取，由 conversationPage 归一形状。
  assert.match(renderer, /conversationPage\(conversationSession, ''\)/);
  assert.match(renderer, /messages\.style\.scrollBehavior = 'auto';/);
  assert.match(renderer, /requestAnimationFrame\(\(\) => \{\s*allowOlderLoad = true;\s*scrollToBottom\(\);/);
});

test('今日简报卡片可以关闭，关掉后同一版不再出现', () => {
  const html = read('index.html');
  const renderer = read('renderer.js');

  assert.match(html, /id="brief-card"/);
  assert.match(html, /id="brief-card-open"/);
  assert.match(html, /id="brief-card-close"/);
  assert.match(html, /关闭今日简报/);
  assert.match(renderer, /dismissedBriefId/);
  assert.match(renderer, /if \(id && id === dismissedBriefId\)/);
  assert.match(renderer, /briefCardClose\?\.addEventListener\('click'/);
});
