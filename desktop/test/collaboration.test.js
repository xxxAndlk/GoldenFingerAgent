'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { CollaborationMode, summarizeObservations } = require('../collaboration');

test('summarizeObservations groups apps, durations and recent window titles', () => {
  const summary = summarizeObservations([
    { app: 'Code', title: 'main.js', at: 0 },
    { app: 'Code', title: 'main.js', at: 30_000 },
    { app: 'Chrome', title: '设计文档', at: 60_000 },
    { app: 'Code', title: 'collaboration.js', at: 90_000 },
  ]);
  assert.match(summary, /Code 约 2 分钟/);
  assert.match(summary, /main\.js；collaboration\.js/);
  assert.match(summary, /Chrome 约 1 分钟/);
});

test('summary input removes repeated and OCR-varied paragraphs', () => {
  const summary = summarizeObservations([
    { app: 'Chrome', title: '项目', content: '个人管家需要记住用户看过的重要内容', at: 0 },
    { app: 'Chrome', title: '项目', content: '个人管家需要记住用户看过的重耍内容', at: 30_000 },
    { app: 'Chrome', title: '项目', content: '这一阶段新增了真正不同的结论', at: 60_000 },
  ]);
  assert.equal((summary.match(/个人管家/g) || []).length, 1);
  assert.match(summary, /真正不同的结论/);
});

test('disabled collaboration never reads the active window', async () => {
  let reads = 0;
  const mode = new CollaborationMode({
    readActiveWindow: async () => { reads += 1; return null; },
    api: {},
  });
  await mode.scanNow();
  assert.equal(reads, 0);
  assert.equal(mode.status().enabled, false);
});

test('due summary is persisted and sent as a proactive conversation', async () => {
  let now = 0;
  const captures = [];
  const chats = [];
  const summaries = [];
  const windows = [
    { owner: { name: 'Code' }, title: 'main.js' },
    { owner: { name: 'Chrome' }, title: '技术方案' },
  ];
  const mode = new CollaborationMode({
    readActiveWindow: async () => windows.shift(),
    api: {
      capture: async (...args) => captures.push(args),
      chat: async text => { chats.push(text); return { reply: '你最近在开发和查资料。要继续吗？' }; },
    },
    onSummary: summary => summaries.push(summary),
    now: () => now,
    summaryIntervalMs: 100,
  });
  await mode.setEnabled(true);
  now = 100;
  await mode.scanNow();
  await mode.setEnabled(false);

  assert.equal(captures.length, 1);
  assert.equal(captures[0][3], 'desktop_collaboration');
  assert.match(captures[0][0], /Code/);
  assert.match(captures[0][0], /Chrome/);
  assert.equal(chats.length, 1);
  assert.match(chats[0], /不要把未采集到的内容猜成事实/);
  assert.equal(summaries.length, 1);
  assert.match(summaries[0].reply, /开发和查资料/);
});

test('failed delivery restores unsaved observations', async () => {
  let now = 0;
  const mode = new CollaborationMode({
    readActiveWindow: async () => ({ owner: { name: 'Code' }, title: 'main.js' }),
    api: { capture: async () => { throw new Error('offline'); } },
    now: () => now,
    summaryIntervalMs: 100,
  });
  await mode.setEnabled(true);
  now = 100;
  await mode.scanNow();
  assert.equal(mode.observations.length, 2);
  assert.ok(mode.summaryDue > now);
  await mode.setEnabled(false);
  assert.equal(mode.observations.length, 0);
});

test('successful summary releases exactly the summarized observation ids', async () => {
  const summarized = [];
  const mode = new CollaborationMode({
    readActiveWindow: async () => null,
    api: {
      capture: async () => {},
      chat: async () => ({ reply: '总结完成' }),
    },
    onSummarized: observations => summarized.push(observations.map(item => item.id)),
  });
  mode.enabled = true;
  mode.addObservation({ id: 'old', app: 'Chrome', title: '页面', content: '本阶段正文', at: 1 });
  const result = await mode.summarizeNow();
  mode.addObservation({ id: 'new', app: 'Chrome', title: '页面', content: '下一阶段正文', at: 2 });
  assert.equal(result.reply, '总结完成');
  assert.deepEqual(summarized, [['old']]);
  assert.deepEqual(mode.observations.map(item => item.id), ['new']);
});

test('summary reuses the active chat session instead of creating one per summary', async () => {
  const sentTo = [];
  const adopted = [];
  const mode = new CollaborationMode({
    readActiveWindow: async () => null,
    api: {
      capture: async () => {},
      chat: async (_text, sessionId) => {
        sentTo.push(sessionId);
        return { reply: '总结完成', session_id: sessionId || 'created-session' };
      },
    },
    sessionId: () => 'active-session',
    onSession: id => adopted.push(id),
  });
  mode.enabled = true;
  mode.addObservation({ id: 'a', app: 'Code', title: 'main.js', content: '正文', at: 1 });
  await mode.summarizeNow();
  assert.deepEqual(sentTo, ['active-session']);
  assert.deepEqual(adopted, []);
});

test('summary adopts the session created for its first turn', async () => {
  const adopted = [];
  const mode = new CollaborationMode({
    readActiveWindow: async () => null,
    api: {
      capture: async () => {},
      chat: async () => ({ reply: '总结完成', session_id: 'created-session' }),
    },
    sessionId: () => '',
    onSession: id => adopted.push(id),
  });
  mode.enabled = true;
  mode.addObservation({ id: 'a', app: 'Code', title: 'main.js', content: '正文', at: 1 });
  await mode.summarizeNow();
  assert.deepEqual(adopted, ['created-session']);
});

test('failed summary does not release observation screenshots', async () => {
  let cleanups = 0;
  const mode = new CollaborationMode({
    readActiveWindow: async () => null,
    api: { capture: async () => { throw new Error('offline'); } },
    onSummarized: () => { cleanups += 1; },
  });
  mode.enabled = true;
  mode.addObservation({ id: 'keep', app: 'Chrome', title: '页面', content: '尚未总结', at: 1 });
  const result = await mode.summarizeNow();
  assert.match(result.error, /offline/);
  assert.equal(cleanups, 0);
  assert.deepEqual(mode.observations.map(item => item.id), ['keep']);
});
