'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { ActivityTrail } = require('../activity-trail');
const { normalizeObservation, observationHash } = require('../observation');
const { NativeActivitySource } = require('../native-activity-source');

test('standard observation includes content identity, active tab and privacy level', () => {
  const observation = normalizeObservation({
    type: 'content', app: 'Chrome', title: '设计稿', activeTab: '设计稿',
    windowTitle: '设计稿 - Chrome', url: 'https://example.com/design',
    content: '当前页面的正文', evidenceRef: 'frame:42', source: 'accessibility', at: 123,
  });
  assert.equal(observation.contentHash, observationHash('当前页面的正文'));
  assert.equal(observation.activeTab, '设计稿');
  assert.equal(observation.evidenceRef, 'frame:42');
  assert.equal(observation.privacyLevel, 'normal');
  assert.equal(observation.source, 'accessibility');
});

test('ActivityTrail consumes an injected source without changing its public contract', async () => {
  const events = [];
  const source = {
    reset() { this.resetCalled = true; },
    stop() { this.stopped = true; },
    async observe() {
      return normalizeObservation({ app: 'Code', title: 'provider.js', content: '可替换采集源已经接入', source: 'mock', at: 100 });
    },
  };
  const trail = new ActivityTrail({
    activitySource: source,
    readActiveWindow: async () => null,
    captureWindow: async () => '',
    evidenceDir: fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-source-')),
    onEvent: event => events.push(event),
  });
  trail.enabled = true;
  await trail.observeWindow();
  assert.equal(events.length, 1);
  assert.equal(events[0].source, 'mock');
  assert.equal(events[0].contentHash, observationHash('可替换采集源已经接入'));
  trail.stop();
  assert.equal(source.stopped, true);
});

test('native source emits a throttled window heartbeat when content is unavailable', async () => {
  let now = 1000;
  const source = new NativeActivitySource({
    readActiveWindow: async () => ({ owner: { name: 'Code' }, title: 'main.js' }),
    readPageContent: async () => ({ text: '', activeTab: '', evidencePath: '', source: 'uia', unchanged: false }),
    now: () => now,
  });
  const first = await source.observe({});
  assert.equal(first.type, 'window');
  assert.equal(first.title, 'main.js');

  now += 6000;
  assert.equal(await source.observe({ force: true }), null);

  now += 60_000;
  const heartbeat = await source.observe({ force: true });
  assert.equal(heartbeat.type, 'window');
  assert.equal(heartbeat.title, 'main.js');
});

test('native source retains page filtering and removes duplicate evidence', async () => {
  const evidenceDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-native-'));
  const evidence = path.join(evidenceDir, 'frame.jpg');
  fs.writeFileSync(evidence, 'mock');
  const source = new NativeActivitySource({
    readActiveWindow: async () => ({ owner: { name: 'Chrome' }, title: '页面 - Chrome' }),
    readPageContent: async () => ({ text: '需要记住的页面正文内容', activeTab: '页面', evidencePath: evidence }),
    now: () => 1000,
  });
  const first = await source.observe({ outputDir: evidenceDir });
  assert.equal(first.content, '需要记住的页面正文内容');
  const second = await source.observe({ force: true, outputDir: evidenceDir });
  assert.equal(second, null);
  assert.equal(fs.existsSync(evidence), false);
});
