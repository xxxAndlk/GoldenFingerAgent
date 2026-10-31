'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  typeMeta,
  isUsefulEvent,
  sanitizeError,
  formatClock,
  summaryExcerpt,
  appLabel,
  appTone,
  appInitial,
} = require('../activity-format');

test('typeMeta maps known and unknown event types', () => {
  assert.deepEqual(typeMeta('content'), { label: '正文', tone: 'blue' });
  assert.deepEqual(typeMeta('submission'), { label: '提交', tone: 'orange' });
  assert.deepEqual(typeMeta('something-new'), { label: '记录', tone: 'gray' });
});

test('isUsefulEvent drops empty and self-repeating placeholder rows', () => {
  assert.equal(isUsefulEvent({ app: 'Idle', title: 'Idle', detail: 'Idle' }), false);
  assert.equal(isUsefulEvent({ app: 'Code', title: '', detail: '' }), false);
  assert.equal(isUsefulEvent({ app: 'Code', title: 'main.go', detail: '正在编辑' }), true);
  assert.equal(isUsefulEvent({ app: 'Code', title: 'main.go', detail: '' }), true);
});

test('sanitizeError turns PowerShell type errors into a friendly notice', () => {
  const raw = [
    '+ CategoryInfo          : InvalidOperation: (GoldenFingerInput:Run) []',
    '+ FullyQualifiedErrorId : TypeNotFound',
    'Unable to find type [GoldenFingerInput].',
  ].join('\n');
  const notice = sanitizeError(raw);
  assert.equal(notice.title, '输入监听未启动');
  assert.match(notice.message, /快捷键与提交记录可能缺失/);
  assert.equal(notice.detail, 'Unable to find type [GoldenFingerInput].');
});

test('sanitizeError keeps a bounded detail for unknown failures', () => {
  const notice = sanitizeError(`boom ${'x'.repeat(900)}`);
  assert.equal(notice.title, '记录遇到问题');
  assert.equal(notice.detail.length, 600);
});

test('formatClock and summaryExcerpt are stable', () => {
  const at = new Date(2026, 9, 30, 18, 5).getTime();
  assert.equal(formatClock(at), '18:05');
  assert.equal(summaryExcerpt('  一行\n两行  ', 20), '一行 两行');
  assert.equal(summaryExcerpt('abcdefghij', 5), 'abcd…');
});

test('app tone and initial are deterministic', () => {
  assert.equal(appInitial('code'), 'V');
  assert.equal(appInitial('终端'), '终');
  assert.equal(appTone('Code'), appTone('Code'));
  assert.ok(['blue', 'green', 'orange', 'purple', 'teal'].includes(appTone('Chrome')));
});

test('appLabel maps known process names and keeps unknown ones', () => {
  assert.equal(appLabel('chrome'), 'Chrome');
  assert.equal(appLabel('WINDOWSTERMINAL'), '终端');
  assert.equal(appLabel('Code'), 'VS Code');
  assert.equal(appLabel('some-new-app'), 'some-new-app');
  assert.equal(appLabel(''), '未知应用');
});
