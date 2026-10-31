'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { ContentMemory } = require('../content-dedup');

test('content memory suppresses OCR typos and Chinese spacing differences', () => {
  const memory = new ContentMemory();
  const first = memory.select('个人管家需要记住用户看过的重要内容');
  const second = memory.select('个人管家 需要记住用户看过的重耍内容');
  assert.match(first, /重要内容/);
  assert.equal(second, '');
});

test('content memory retains genuinely new paragraphs and changed numbers', () => {
  const memory = new ContentMemory();
  memory.select('本阶段已经完成页面相似内容去重并准备继续测试');
  assert.match(memory.select('下一阶段需要验证提交动作和阶段总结的实际效果'), /下一阶段/);
  memory.select('当前构建已通过 40 项自动化测试');
  assert.match(memory.select('当前构建已通过 41 项自动化测试'), /41/);
});

test('short labels require an exact match to avoid merging different actions', () => {
  const memory = new ContentMemory();
  memory.select('允许访问');
  assert.equal(memory.select('取消访问'), '取消访问');
});
