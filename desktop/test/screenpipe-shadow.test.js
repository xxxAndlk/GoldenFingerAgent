'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { ScreenpipeSource, parseScreenpipeItem } = require('../screenpipe-source');
const { ScreenpipeShadow } = require('../screenpipe-shadow');

const at = Date.parse('2026-10-29T10:00:00.000Z');

test('Screenpipe parser accepts nested accessibility data and preserves the focused tab', () => {
  const observation = parseScreenpipeItem({
    type: 'Accessibility',
    content: {
      text: '当前正文包含中文工作内容', app_name: 'Chrome', window_name: '项目设计 - Chrome',
      browser_url: 'https://example.com/current', browser_title: '项目设计', frame_id: 42,
      focused: true, timestamp: '2026-10-29T10:00:00.000Z',
    },
  });
  assert.equal(observation.activeTab, '项目设计');
  assert.equal(observation.url, 'https://example.com/current');
  assert.equal(observation.evidenceRef, 'screenpipe:frame:42');
  assert.equal(observation.source, 'screenpipe:accessibility');
});

test('Screenpipe parser ignores background windows and sensitive titles', () => {
  assert.equal(parseScreenpipeItem({ type: 'OCR', content: { text: '后台页面', focused: false } }), null);
  const sensitive = parseScreenpipeItem({ type: 'OCR', content: {
    text: 'secret', app_name: '1Password', window_name: '密码库', focused: true, timestamp: at,
  } });
  assert.equal(sensitive.privacyLevel, 'protected');
});

test('Screenpipe source uses documented API, authentication and content deduplication', async () => {
  const requests = [];
  const fetchImpl = async (url, options) => {
    requests.push({ url, options });
    const type = new URL(url).searchParams.get('content_type');
    const data = type === 'accessibility' ? [{
      type: 'Accessibility', content: {
        text: '中文页面正文', app_name: 'Chrome', window_name: '当前 Tab', focused: true,
        timestamp: '2026-10-29T10:00:00.000Z', browser_url: 'https://example.com',
      },
    }] : [];
    return { ok: true, json: async () => ({ data }) };
  };
  const source = new ScreenpipeSource({ fetchImpl, apiKey: 'local-key', now: () => at });
  const first = await source.poll();
  assert.equal(first.observations.length, 1);
  assert.equal(requests.length, 3);
  assert.equal(requests[0].options.headers.Authorization, 'Bearer local-key');
  assert.equal(requests[0].options.headers['X-Screenpipe-Client'], 'api');
  source.cursor = at - 1000;
  const second = await source.poll();
  assert.equal(second.observations.length, 0);
  assert.equal(second.duplicates, 1);
});

test('shadow records metrics separately without emitting native timeline events', async () => {
  const outputDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-shadow-'));
  const source = {
    reset() {},
    stop() {},
    async poll() {
      return { received: 2, duplicates: 1, observations: [
        parseScreenpipeItem({ type: 'Accessibility', content: {
          text: '正在进行中文接口改造', app_name: 'Code', window_name: 'provider.js', focused: true, timestamp: at,
        } }),
        parseScreenpipeItem({ type: 'Input', content: {
          event_type: 'text', text: '请总结当前工作', app_name: 'Code', window_name: 'provider.js', focused: true, timestamp: at,
        } }),
      ] };
    },
  };
  const shadow = new ScreenpipeShadow({ source, outputDir, now: () => at });
  shadow.addNative({ app: 'Code', title: 'provider.js', at, content: '原生轨迹不应被修改' });
  shadow.enabled = true;
  fs.mkdirSync(outputDir, { recursive: true });
  await shadow.poll();
  const status = shadow.status();
  assert.equal(status.mode, 'shadow');
  assert.equal(status.metrics.samples, 2);
  assert.equal(status.metrics.duplicateRate, 0.5);
  assert.equal(status.metrics.tabMatchRate, 1);
  assert.equal(status.metrics.inputRestoreRate, 1);
  assert.ok(status.metrics.cjkRatio > 0);
  const rows = fs.readFileSync(path.join(outputDir, 'observations.jsonl'), 'utf8').trim().split('\n');
  assert.equal(rows.length, 2);
  assert.equal(JSON.parse(rows[0]).observation.source, 'screenpipe:accessibility');
});

test('shadow drops protected observations before writing content', async () => {
  const outputDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-shadow-private-'));
  const shadow = new ScreenpipeShadow({
    source: { reset() {}, stop() {}, async poll() {
      return { received: 1, duplicates: 0, observations: [parseScreenpipeItem({ type: 'OCR', content: {
        text: '不应保存', app_name: '银行', window_name: '支付密码', focused: true, timestamp: at,
      } })] };
    } },
    outputDir,
    now: () => at,
  });
  shadow.enabled = true;
  await shadow.poll();
  assert.equal(shadow.status().metrics.privacyBlocked, 1);
  assert.equal(fs.existsSync(path.join(outputDir, 'observations.jsonl')), false);
});
