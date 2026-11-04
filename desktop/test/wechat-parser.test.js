'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  parseWeChatChat, alignVisibleMessages, normalizeLine, similarity,
} = require('../wechat-parser');

const REAL_BOXES = [
  { text: '严 胜 易', x: 795, y: 118, width: 103, height: 32 },
  { text: '运 营 · 陈 桂 英', x: 303, y: 509, width: 190, height: 32 },
  { text: '严 胜 易', x: 303, y: 1157, width: 103, height: 31 },
  { text: '， 你 妈 兵', x: 973, y: 757, width: 244, height: 51 },
  { text: '你 妈 美 国 兵', x: 1052, y: 1133, width: 165, height: 36 },
  { text: '0', x: 1209, y: 1485, width: 31, height: 43 },
];

test('WeChat parser keeps only the open conversation messages and normalizes OCR spaces', () => {
  const parsed = parseWeChatChat({ boxes: REAL_BOXES, width: 2207, height: 1600, now: new Date('2026-11-03T00:00:00Z') });
  assert.equal(parsed.title, '严胜易');
  assert.equal(parsed.is_group, false);
  assert.equal(parsed.completeness, 'current_visible');
  assert.equal(parsed.messages.length, 2);
  assert.deepEqual(parsed.messages.map(item => item.text), [', 你妈兵', '你妈美国兵']);
  assert.ok(parsed.messages.every(item => item.direction === 'other'));
  assert.ok(parsed.messages.every(item => item.sender === '严胜易'));
  assert.equal(parsed.messages[0].occurred_at, null);
});

test('WeChat parser keeps short numeric messages but drops timestamps and UI labels', () => {
  const parsed = parseWeChatChat({
    width: 1000,
    height: 800,
    boxes: [
      { text: '测试会话', x: 360, y: 20, width: 100, height: 20 },
      { text: '00:13', x: 460, y: 70, width: 50, height: 16 },
      { text: '1', x: 360, y: 120, width: 10, height: 18 },
      { text: '发消息', x: 360, y: 700, width: 60, height: 18 },
    ],
  });
  assert.equal(parsed.title, '测试会话');
  assert.deepEqual(parsed.messages.map(item => item.text), ['1']);
});

test('Group chats keep an unknown sender instead of guessing', () => {
  const parsed = parseWeChatChat({
    width: 1000,
    height: 800,
    boxes: [
      { text: '项目群 (3)', x: 360, y: 20, width: 120, height: 20 },
      { text: '周五发版', x: 360, y: 120, width: 80, height: 20 },
    ],
  });
  assert.equal(parsed.is_group, true);
  assert.equal(parsed.messages[0].sender, '');
  assert.equal(parsed.messages[0].direction, 'other');
});

test('Repeated identical messages remain separate messages', () => {
  const first = alignVisibleMessages([], [
    { text: '收到', direction: 'other' },
    { text: '收到', direction: 'other' },
  ]);
  assert.equal(first.messages.length, 2);
  assert.notEqual(first.messages[0].event_id, first.messages[1].event_id);

  const second = alignVisibleMessages(first.messages, [
    { text: '收到', direction: 'other' },
    { text: '收到', direction: 'other' },
    { text: '收到', direction: 'other' },
  ]);
  assert.equal(second.aligned, true);
  assert.equal(second.overlap, 2);
  assert.equal(second.messages.length - second.overlap, 1);
});

test('OCR drift still aligns instead of importing the same history again', () => {
  assert.ok(similarity('我要困死了', '我要国死了') >= 0.7);
  const previous = [{ text: '我要困死了', direction: 'other', event_id: 'e1' }];
  const aligned = alignVisibleMessages(previous, [{ text: '我要国死了', direction: 'other' }]);
  assert.equal(aligned.aligned, true);
  assert.equal(aligned.overlap, 1);
  assert.equal(aligned.messages[0].event_id, 'e1');
});

test('Unrelated visible history is marked as a gap rather than duplicated', () => {
  const aligned = alignVisibleMessages(
    [{ text: '上一条消息', direction: 'other', event_id: 'e1' }],
    [{ text: '完全不同的消息', direction: 'other' }],
  );
  assert.equal(aligned.aligned, false);
  assert.equal(aligned.gap, true);
  assert.equal(aligned.messages.length, 1);
});

test('normalizeLine removes spaces between Chinese characters but keeps English word spacing', () => {
  assert.equal(normalizeLine('你 妈 美 国 兵'), '你妈美国兵');
  assert.equal(normalizeLine('Hello  World'), 'Hello World');
});
