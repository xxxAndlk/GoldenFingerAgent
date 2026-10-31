'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { selectNovelContent, contentExcerpt } = require('../activity-trail');

test('repeated fixed panels are not recorded again across applications', () => {
  const seen = new Set();
  const first = selectNovelContent('总额度 67.65%\n用户正在讨论通用桌面内容采集方案', seen);
  const second = selectNovelContent('总额度 67.65%\n用户正在讨论通用桌面内容采集方案', seen);
  assert.match(first, /通用桌面内容采集方案/);
  assert.equal(second, '');
});

test('content excerpt prefers meaningful paragraphs over URLs and percentages', () => {
  const excerpt = contentExcerpt('https://example.com/settings\n总额度 67.65%\n我们需要整理项目需求并确定下一步实施计划');
  assert.match(excerpt, /整理项目需求/);
  assert.doesNotMatch(excerpt, /https:/);
});
