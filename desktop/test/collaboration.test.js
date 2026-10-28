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
  assert.match(chats[0], /不要推测标题之外的操作细节/);
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
