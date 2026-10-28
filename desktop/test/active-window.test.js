'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { parseActiveWindowOutput } = require('../active-window');

test('active window parser preserves Chinese title and app name', () => {
  const value = parseActiveWindowOutput('{"title":"金手指项目 — 技术方案","owner":{"name":"Code"}}\n');
  assert.deepEqual(value, { title: '金手指项目 — 技术方案', owner: { name: 'Code' } });
});

test('active window parser handles no foreground window', () => {
  assert.equal(parseActiveWindowOutput(''), null);
});
