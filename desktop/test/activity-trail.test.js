'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { ActivityTrail, publicEvent } = require('../activity-trail');

test('publicEvent never exposes local evidence path', () => {
  const event = publicEvent({ id: '1', detail: '点击保存', evidencePath: 'C:\\private\\one.jpg' });
  assert.equal(event.hasEvidence, true);
  assert.equal('evidencePath' in event, false);
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

test('ActivityTrail deletes only screenshots included in a successful summary', () => {
  const evidenceDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-trail-'));
  const summarizedFile = path.join(evidenceDir, 'summarized.jpg');
  const pendingFile = path.join(evidenceDir, 'pending.jpg');
  fs.writeFileSync(summarizedFile, 'summarized');
  fs.writeFileSync(pendingFile, 'pending');
  const trail = new ActivityTrail({ evidenceDir });
  trail.events = [
    { id: 'old', evidencePath: summarizedFile },
    { id: 'new', evidencePath: pendingFile },
  ];
  trail.deleteSummarizedEvidence(['old']);
  assert.equal(fs.existsSync(summarizedFile), false);
  assert.equal(fs.existsSync(pendingFile), true);
  assert.equal(trail.events[0].evidencePath, '');
  assert.equal(trail.events[1].evidencePath, pendingFile);
});
