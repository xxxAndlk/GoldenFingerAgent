'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  CollaborationMode, summarizeObservations, stripTrailingQuestion, segmentObservations,
} = require('../collaboration');

test('segmentObservations splits by context and idle gap instead of one daily bucket', () => {
  const segments = segmentObservations([
    { app: 'Code', title: 'a', at: 0, contextKey: 'project:Alpha', projectCandidates: ['Alpha'] },
    { app: 'Chrome', title: 'b', at: 1000, contextKey: 'project:Alpha' },
    { app: 'Code', title: 'c', at: 2000, contextKey: 'project:Beta', projectCandidates: ['Beta'] },
    { app: 'Code', title: 'd', at: 2_000_000, contextKey: 'project:Beta' },
  ], { idleGapMs: 60_000 });
  assert.equal(segments.length, 3);
  assert.equal(segments[0].contextKey, 'project:Alpha');
  assert.deepEqual(segments[0].apps, ['Code', 'Chrome']);
  assert.equal(segments[1].contextKey, 'project:Beta');
  assert.equal(segments[2].contextKey, 'project:Beta');
});

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

test('summary strips a trailing question so it stays a summary', () => {
  assert.equal(stripTrailingQuestion('本阶段完成。\n\n要不要我继续？'), '本阶段完成。');
  assert.equal(stripTrailingQuestion('本阶段完成。要不要我继续？'), '本阶段完成。');
  assert.equal(stripTrailingQuestion('要不要我继续？'), '');
});

test('summary input keeps details beyond the old 60-item window', () => {
  const observations = Array.from({ length: 70 }, (_, index) => ({
    app: 'Code',
    title: `文件${index}`,
    content: `第${index}条不同内容`,
    at: index * 30_000,
  }));
  const summary = summarizeObservations(observations);
  assert.match(summary, /第0条不同内容/);
  assert.match(summary, /第69条不同内容/);
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
  const episodes = [];
  const windows = [
    { owner: { name: 'Code' }, title: 'main.js' },
    { owner: { name: 'Chrome' }, title: '技术方案' },
  ];
  const mode = new CollaborationMode({
    readActiveWindow: async () => windows.shift(),
    api: {
      capture: async (...args) => captures.push(args),
      chat: async text => { chats.push(text); return { reply: '你最近在开发和查资料。要继续吗？' }; },
      captureActivityEpisode: async payload => episodes.push(payload),
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
  assert.equal(episodes.length, 1);
  assert.match(episodes[0].summary, /开发和查资料/);
  assert.equal(episodes[0].status, 'completed');
  assert.ok(episodes[0].episode_id);
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

test('summary reply and phase do not retain a final question', async () => {
  const phases = [];
  const chats = [];
  const mode = new CollaborationMode({
    readActiveWindow: async () => null,
    api: {
      capture: async () => {},
      chat: async text => {
        chats.push(text);
        return { reply: '本阶段完成。\n\n要不要我继续？' };
      },
    },
    onPhase: phase => phases.push(phase),
  });
  mode.enabled = true;
  mode.addObservation({ id: 'a', app: 'Code', title: 'main.js', content: '这是本阶段正文，包含足够的内容用于摘要。', at: 1 });
  const result = await mode.summarizeNow();
  assert.equal(result.reply, '本阶段完成。');
  assert.equal(phases[0].summary, '本阶段完成。');
  assert.doesNotMatch(chats[0], /最后问一个帮助用户继续工作的问题/);
});

test('summary writes a traceable phase record and carries the previous summary', async () => {
  const phases = [];
  const chats = [];
  const mode = new CollaborationMode({
    readActiveWindow: async () => null,
    api: {
      capture: async () => {},
      chat: async text => { chats.push(text); return { reply: '本阶段结论' }; },
    },
    loadLastSummary: () => '上一阶段结论',
    sessionId: () => 'chat-1',
    onPhase: phase => phases.push(phase),
  });
  mode.enabled = true;
  mode.addObservation({ id: 'a', app: 'Code', title: 'main.js', content: '这是本阶段正文 A，包含足够的内容用于摘要。', at: 1 });
  mode.addObservation({ id: 'b', app: 'Chrome', title: '文档', content: '这是本阶段正文 B，包含足够的内容用于摘要。', at: 2 });
  const result = await mode.summarizeNow();
  assert.equal(result.phase_id, phases[0].id);
  assert.deepEqual(phases[0].eventIds, ['a', 'b']);
  assert.equal(phases[0].sessionId, 'chat-1');
  assert.equal(phases[0].summary, '本阶段结论');
  assert.match(chats[0], /上一阶段结论/);
  assert.match(chats[0], /正文 A/);
});

test('clear removes pending observations and previous summary', () => {
  const mode = new CollaborationMode({
    readActiveWindow: async () => null,
    api: {},
    loadLastSummary: () => '旧结论',
  });
  mode.enabled = true;
  mode.lastSummary = '旧结论';
  mode.addObservation({ id: 'a', app: 'Code', title: 'main.js', at: 1 });
  mode.clear();
  assert.equal(mode.observations.length, 0);
  assert.equal(mode.lastSummary, '');
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
