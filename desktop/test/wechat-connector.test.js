'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { WeChatConnector, readPngSize } = require('../wechat-connector');

function png(width, height) {
  const buffer = Buffer.alloc(24);
  buffer.writeUInt32BE(0x89504e47, 0);
  buffer.writeUInt32BE(0x0d0a1a0a, 4);
  buffer.writeUInt32BE(width, 16);
  buffer.writeUInt32BE(height, 20);
  return buffer;
}

function fixture() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-wechat-'));
  let boxes = [
    { text: '严 胜 易', x: 360, y: 20, width: 100, height: 24 },
    { text: '你 好', x: 360, y: 120, width: 80, height: 24 },
    { text: '在 吗', x: 360, y: 200, width: 80, height: 24 },
  ];
  const queued = [];
  const spool = {
    enqueue: record => { queued.push(record); return record; },
    status: () => ({ pending: 0, filtered: 0, failed: 0, lastDeliveredAt: '', lastError: '' }),
  };
  let enabled = true;
  let allowed = true;
  const connector = new WeChatConnector({
    statePath: path.join(dir, 'state.json'),
    captureDir: dir,
    spool,
    isEnabled: async () => enabled,
    isSourceAllowed: async () => allowed,
    readWindow: async () => ({ title: '微信', rect: { x: 0, y: 0, width: 1000, height: 800 } }),
    capture: async ({ outputDir }) => {
      const file = path.join(outputDir, `cap-${Date.now()}-${Math.random()}.png`);
      fs.mkdirSync(outputDir, { recursive: true });
      fs.writeFileSync(file, png(1000, 800));
      return file;
    },
    ocr: async () => ({ text: '', lines: [], boxes }),
    now: () => 1000,
  });
  return {
    dir,
    connector,
    queued,
    setBoxes(next) { boxes = next; },
    setEnabled(value) { enabled = value; },
    setAllowed(value) { allowed = value; },
  };
}

test('WeChat connector enqueues visible messages once and only appends new suffix', async () => {
  const fx = fixture();
  try {
    const first = await fx.connector.processCapture();
    assert.equal(first.ok, true);
    assert.equal(first.enqueued, 2);
    assert.equal(fx.queued.length, 2);
    assert.equal(fx.queued[0].payload.source, '严胜易');
    assert.equal(fx.queued[0].payload.completeness, 'current_visible');
    assert.equal(fx.queued[0].payload.occurred_at, null);
    assert.ok(fx.queued[0].payload.event_id);

    const second = await fx.connector.processCapture();
    assert.equal(second.enqueued, 0);
    assert.equal(fx.queued.length, 2);
    assert.equal(fx.connector.status().state, 'observing');

    fx.setBoxes([
      { text: '严 胜 易', x: 360, y: 20, width: 100, height: 24 },
      { text: '你 好', x: 360, y: 120, width: 80, height: 24 },
      { text: '在 吗', x: 360, y: 200, width: 80, height: 24 },
      { text: '晚 上 见', x: 360, y: 280, width: 100, height: 24 },
    ]);
    const third = await fx.connector.processCapture();
    assert.equal(third.enqueued, 1);
    assert.equal(fx.queued.length, 3);
    assert.equal(fx.queued[2].payload.text, '晚上见');
  } finally {
    fs.rmSync(fx.dir, { recursive: true, force: true });
  }
});

test('WeChat connector keeps repeated identical texts and marks an unalignable gap', async () => {
  const fx = fixture();
  try {
    fx.setBoxes([
      { text: '测 试', x: 360, y: 20, width: 80, height: 24 },
      { text: '收 到', x: 360, y: 120, width: 60, height: 24 },
      { text: '收 到', x: 360, y: 200, width: 60, height: 24 },
    ]);
    const first = await fx.connector.processCapture();
    assert.equal(first.enqueued, 2);
    assert.equal(fx.queued[0].payload.message_id === fx.queued[1].payload.message_id, false);

    fx.setBoxes([
      { text: '测 试', x: 360, y: 20, width: 80, height: 24 },
      { text: '另 一 条', x: 360, y: 120, width: 100, height: 24 },
    ]);
    const gap = await fx.connector.processCapture();
    assert.equal(gap.gap, true);
    assert.equal(gap.enqueued, 0);
    assert.equal(fx.connector.status().state, 'degraded');
  } finally {
    fs.rmSync(fx.dir, { recursive: true, force: true });
  }
});

test('WeChat connector keeps separate checkpoints when the open conversation changes', async () => {
  const fx = fixture();
  try {
    await fx.connector.processCapture();
    assert.equal(fx.queued.length, 2);
    fx.setBoxes([
      { text: '另 一 个 会 话', x: 360, y: 20, width: 140, height: 24 },
      { text: '新 对 话', x: 360, y: 120, width: 90, height: 24 },
    ]);
    const switched = await fx.connector.processCapture();
    assert.equal(switched.enqueued, 1);
    assert.equal(fx.queued[2].payload.source, '另一个会话');
    fx.setBoxes([
      { text: '严 胜 易', x: 360, y: 20, width: 100, height: 24 },
      { text: '你 好', x: 360, y: 120, width: 80, height: 24 },
      { text: '在 吗', x: 360, y: 200, width: 80, height: 24 },
    ]);
    const back = await fx.connector.processCapture();
    assert.equal(back.enqueued, 0);
    assert.equal(fx.queued.length, 3);
  } finally {
    fs.rmSync(fx.dir, { recursive: true, force: true });
  }
});

test('WeChat connector does not read or enqueue unauthorized conversations', async () => {
  const fx = fixture();
  try {
    fx.setAllowed(false);
    const result = await fx.connector.processCapture();
    assert.equal(result.ok, false);
    assert.equal(result.stage, 'authorization');
    assert.equal(fx.queued.length, 0);
    assert.equal(fx.connector.status().state, 'waiting_authorization');
  } finally {
    fs.rmSync(fx.dir, { recursive: true, force: true });
  }
});

test('WeChat connector waits for a conversation instead of reporting a parse error', async () => {
  const fx = fixture();
  try {
    fx.setBoxes([{ text: '啊 璃', x: 100, y: 120, width: 80, height: 24 }]);
    const result = await fx.connector.processCapture();
    assert.equal(result.ok, false);
    assert.equal(result.stage, 'waiting_conversation');
    assert.match(result.error, /没有打开任何会话/);
    assert.equal(fx.connector.status().state, 'waiting_conversation');
    assert.equal(fx.queued.length, 0);
  } finally {
    fs.rmSync(fx.dir, { recursive: true, force: true });
  }
});

test('test capture reports a preview without enqueueing observations', async () => {
  const fx = fixture();
  try {
    const result = await fx.connector.testCapture();
    assert.equal(result.ok, true);
    assert.equal(result.title, '严胜易');
    assert.equal(result.messageCount, 2);
    assert.equal(fx.queued.length, 0);
  } finally {
    fs.rmSync(fx.dir, { recursive: true, force: true });
  }
});

test('readPngSize validates the PNG header', () => {
  const fx = fixture();
  try {
    const file = path.join(fx.dir, 'x.png');
    fs.writeFileSync(file, png(640, 480));
    assert.deepEqual(readPngSize(file), { width: 640, height: 480 });
    fs.writeFileSync(file, 'not a png');
    assert.equal(readPngSize(file), null);
  } finally {
    fs.rmSync(fx.dir, { recursive: true, force: true });
  }
});
