'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { ActivityTrail, publicEvent, EVIDENCE_TTL_MS } = require('../activity-trail');

test('publicEvent never exposes local evidence path', () => {
  const event = publicEvent({ id: '1', detail: '点击保存', evidencePath: 'C:\\private\\one.jpg' });
  assert.equal(event.hasEvidence, true);
  assert.equal('evidencePath' in event, false);
});

test('ActivityTrail chatContext includes only today summaries and events', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-context-'));
  const today = new Date(2026, 10, 1, 12, 0, 0);
  const todayAt = new Date(2026, 10, 1, 10, 30, 0).getTime();
  const yesterdayAt = new Date(2026, 9, 31, 10, 30, 0).getTime();
  fs.writeFileSync(path.join(dir, 'activity-events.jsonl'), [
    { id: 'today', at: todayAt, app: 'Chrome', title: '设计方案', detail: '完成设置页' },
    { id: 'old', at: yesterdayAt, app: 'Chrome', title: '昨天旧记录', detail: '不应出现' },
  ].map(item => JSON.stringify(item)).join('\n') + '\n');
  fs.writeFileSync(path.join(dir, 'activity-summaries.jsonl'), [
    { summary: '今天完成了设置页和轨迹窗', completedAt: todayAt },
    { summary: '昨天的旧总结', completedAt: yesterdayAt },
  ].map(item => JSON.stringify(item)).join('\n') + '\n');

  const trail = new ActivityTrail({
    readActiveWindow: async () => ({ owner: { name: 'Chrome' }, title: '设计方案' }),
    captureWindow: async () => '',
    evidenceDir: dir,
    dataDir: dir,
    now: () => today.getTime(),
  });
  const context = trail.chatContext({ now: today.getTime() });
  assert.match(context, /今天完成了设置页和轨迹窗/);
  assert.match(context, /完成设置页/);
  assert.doesNotMatch(context, /昨天旧总结|昨天旧记录/);
});

test('ActivityTrail records visible page content without duplicate polling events', async () => {
  const events = [];
  const trail = new ActivityTrail({
    readActiveWindow: async () => ({ owner: { name: 'Chrome' }, title: '设计方案 - Google Chrome' }),
    readPageContent: async () => ({ text: '个人管家需要记住用户看过的重要内容。', activeTab: '设计方案', url: 'https://example.com/design', protectedContent: false }),
    captureWindow: async () => '',
    evidenceDir: fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-')),
    onEvent: event => events.push(event),
  });
  trail.enabled = true;
  await trail.observeWindow();
  await trail.observeWindow(true);
  assert.equal(events.length, 1);
  assert.equal(events[0].type, 'content');
  assert.equal(events[0].title, '设计方案');
  assert.equal(events[0].windowTitle, '设计方案 - Google Chrome');
  assert.equal(events[0].url, 'https://example.com/design');
  assert.match(events[0].detail, /记住用户看过的重要内容/);
  trail.stop();
});

test('ActivityTrail does not turn ordinary clicks into timeline noise', async () => {
  const events = [];
  const trail = new ActivityTrail({
    readActiveWindow: async () => ({ owner: { name: 'Chrome' }, title: '设计方案' }),
    readPageContent: async () => ({ text: '', protectedContent: false }),
    readUiContext: async () => ({}),
    captureWindow: async () => '',
    evidenceDir: fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-')),
    onEvent: event => events.push(event),
  });
  trail.enabled = true;
  await trail.processInput({ type: 'click', x: 10, y: 10 });
  assert.equal(events.length, 0);
  trail.stop();
});

test('ActivityTrail merges OCR variations instead of adding another page event', async () => {
  const events = [];
  let text = '个人管家需要记住用户看过的重要内容';
  const trail = new ActivityTrail({
    readActiveWindow: async () => ({ owner: { name: 'Chrome' }, title: '设计方案' }),
    readPageContent: async () => ({ text, protectedContent: false }),
    captureWindow: async () => '',
    evidenceDir: fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-')),
    onEvent: event => events.push(event),
  });
  trail.enabled = true;
  await trail.observeWindow();
  text = '个人管家需要记住用户看过的重耍内容';
  await trail.observeWindow(true);
  assert.equal(events.length, 1);
  trail.stop();
});

test('ActivityTrail records submitted text instead of a bare Enter event', async () => {
  const events = [];
  const trail = new ActivityTrail({
    readActiveWindow: async () => ({ owner: { name: 'Chrome' }, title: 'Agent Admin' }),
    captureWindow: async () => '',
    evidenceDir: fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-')),
    onEvent: event => events.push(event),
  });
  trail.enabled = true;
  const input = { type: 'shortcut', key: 'Enter', text: '请帮我整理今天的项目进展和待办事项' };
  await trail.processInput(input);
  await trail.processInput(input);
  assert.equal(events.length, 1);
  assert.equal(events[0].type, 'submission');
  assert.equal(events[0].content, input.text);
  assert.doesNotMatch(events[0].detail, /^Enter$/);
  trail.stop();
});

test('ActivityTrail does not create a submission when the editor has no real value', async () => {
  const events = [];
  const trail = new ActivityTrail({
    readActiveWindow: async () => ({ owner: { name: 'Chrome' }, title: 'Agent Admin' }),
    captureWindow: async () => '',
    evidenceDir: fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-')),
    onEvent: event => events.push(event),
  });
  trail.enabled = true;
  await trail.processInput({ type: 'shortcut', key: 'Enter', text: '' });
  assert.equal(events.length, 0);
  trail.stop();
});

test('ActivityTrail records meaningful button actions but not ordinary clicks', async () => {
  const events = [];
  const trail = new ActivityTrail({
    readActiveWindow: async () => ({ owner: { name: 'Code' }, title: '项目' }),
    readUiContext: async () => ({ name: '保存', control: 'Button', isPassword: false }),
    captureWindow: async () => '',
    evidenceDir: fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-')),
    onEvent: event => events.push(event),
  });
  trail.enabled = true;
  await trail.processInput({ type: 'click', x: 100, y: 100 });
  assert.equal(events.length, 1);
  assert.equal(events[0].type, 'action');
  assert.match(events[0].detail, /保存/);
  trail.stop();
});

test('ActivityTrail keeps summarized screenshots for three days then deletes them', () => {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-data-'));
  const evidenceDir = path.join(dataDir, 'evidence');
  fs.mkdirSync(evidenceDir, { recursive: true });
  const summarizedFile = path.join(evidenceDir, 'summarized.jpg');
  const pendingFile = path.join(evidenceDir, 'pending.jpg');
  fs.writeFileSync(summarizedFile, 'summarized');
  fs.writeFileSync(pendingFile, 'pending');
  const summaryAt = 1_000_000;
  const later = summaryAt + EVIDENCE_TTL_MS + 1;
  const trail = new ActivityTrail({ evidenceDir, dataDir, now: () => later });
  trail.events = [
    { id: 'old', at: summaryAt - 10, evidencePath: summarizedFile },
    { id: 'new', at: later - 1000, evidencePath: pendingFile },
  ];
  trail.markSummarized(['old'], 'phase-1', summaryAt);
  assert.equal(fs.existsSync(summarizedFile), true);
  assert.equal(trail.events[0].summarizedAt, summaryAt);
  assert.equal(trail.events[0].evidenceExpiresAt, summaryAt + EVIDENCE_TTL_MS);
  trail.removeExpiredEvidence();
  assert.equal(fs.existsSync(summarizedFile), false);
  assert.equal(fs.existsSync(pendingFile), true);
  assert.equal(trail.events[0].evidencePath, '');
  assert.equal(trail.events[1].evidencePath, pendingFile);
});

test('ActivityTrail clear removes persisted events and summary phases', async () => {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-data-'));
  const evidenceDir = path.join(dataDir, 'evidence');
  fs.mkdirSync(evidenceDir, { recursive: true });
  const trail = new ActivityTrail({ evidenceDir, dataDir, now: () => 1000 });
  await trail.record({ type: 'content', app: 'Code', title: 'main.js', at: 1000 }, false);
  trail.saveSummaryPhase({ id: 'phase-1', summary: '旧阶段' });
  trail.clear();
  const restored = new ActivityTrail({ evidenceDir, dataDir, now: () => 1001 });
  assert.equal(restored.list().length, 0);
  assert.equal(restored.lastSummary(), '');
});

test('ActivityTrail restores persisted events and summary marks after restart', async () => {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-data-'));
  const evidenceDir = path.join(dataDir, 'evidence');
  fs.mkdirSync(evidenceDir, { recursive: true });
  const trail = new ActivityTrail({ evidenceDir, dataDir, now: () => 1000 });
  const entry = await trail.record({ type: 'content', app: 'Code', title: 'main.js', at: 1000 }, false);
  trail.markSummarized([entry.id], 'phase-1', 1000);
  trail.saveSummaryPhase({ id: 'phase-1', summary: '这一阶段完成了持久化' });
  const restored = new ActivityTrail({ evidenceDir, dataDir, now: () => 1001 });
  assert.equal(restored.list().length, 1);
  assert.equal(restored.list()[0].title, 'main.js');
  assert.equal(restored.list()[0].summarized, true);
  assert.equal(restored.lastSummary(), '这一阶段完成了持久化');
  assert.deepEqual(restored.listSummaryPhases().map(phase => phase.summary), ['这一阶段完成了持久化']);
});
