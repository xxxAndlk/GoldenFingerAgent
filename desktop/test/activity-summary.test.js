'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { wantsActivitySummary } = require('../activity-summary');

test('activity summary intent only matches work-summary questions', () => {
  assert.equal(wantsActivitySummary('今天做了哪些工作？'), true);
  assert.equal(wantsActivitySummary('总结一下今天的工作'), true);
  assert.equal(wantsActivitySummary('看看我今天做事的细节'), true);
  assert.equal(wantsActivitySummary('你是谁'), false);
  assert.equal(wantsActivitySummary('今天天气怎么样'), false);
  assert.equal(wantsActivitySummary('总结一下这段代码'), false);
});
