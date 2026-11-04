'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { ObservationSpool } = require('../observation-spool');

function tempFile(name) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-spool-'));
  return { dir, file: path.join(dir, name) };
}

test('spool marks confirmed observations delivered and persists them', async () => {
  const { dir, file } = tempFile('spool.json');
  try {
    let now = 1000;
    const delivered = [];
    const spool = new ObservationSpool({
      filePath: file,
      now: () => now,
      deliver: async record => {
        delivered.push(record.payload.event_id);
        return { status: 201, body: { created: true } };
      },
    });
    spool.enqueue({ endpoint: '/x', payload: { event_id: 'e1', text: 'hello' }, kind: 'test' });
    await spool.flush();
    assert.deepEqual(delivered, ['e1']);
    assert.equal(spool.status().pending, 0);
    const saved = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.equal(saved[0].status, 'delivered');
    const restored = new ObservationSpool({ filePath: file, now: () => now, deliver: async () => ({ status: 200, body: {} }) });
    assert.equal(restored.status().delivered, 1);
    assert.ok(restored.status().lastDeliveredAt);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('spool drops body after policy filtering or permanent rejection', async () => {
  const { dir, file } = tempFile('spool.json');
  try {
    const spool = new ObservationSpool({
      filePath: file,
      deliver: async record => ({ status: 202, body: { filtered: true, reason: record.payload.event_id === 'e1' ? 'disabled' : 'denied' } }),
    });
    spool.enqueue({ endpoint: '/x', payload: { event_id: 'e1', text: 'secret' } });
    await spool.flush();
    assert.equal(spool.status().filtered, 1);
    assert.equal(spool.records[0].payload.dropped, true);
    assert.equal(spool.records[0].payload.text, undefined);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('spool retries server errors and restores pending work after restart', async () => {
  const { dir, file } = tempFile('spool.json');
  try {
    let now = 1000;
    let fail = true;
    const first = new ObservationSpool({
      filePath: file,
      now: () => now,
      deliver: async () => {
        if (fail) throw new Error('offline');
        return { status: 200, body: { created: false } };
      },
    });
    first.enqueue({ endpoint: '/x', payload: { event_id: 'e1', text: 'hello' } });
    await first.flush();
    assert.equal(first.status().pending, 1);
    assert.equal(first.records[0].attempts, 1);

    const restored = new ObservationSpool({ filePath: file, now: () => now, deliver: async () => ({ status: 200, body: {} }) });
    assert.equal(restored.status().pending, 1);
    now = first.records[0].next_attempt_at + 1;
    fail = false;
    await restored.flush();
    assert.equal(restored.status().pending, 0);
    assert.equal(restored.status().delivered, 1);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('spool treats 4xx as permanent and clears pending body', async () => {
  const { dir, file } = tempFile('spool.json');
  try {
    const spool = new ObservationSpool({
      filePath: file,
      deliver: async () => ({ status: 400, body: { error: 'bad request' } }),
    });
    spool.enqueue({ endpoint: '/x', payload: { event_id: 'e1', text: 'hello' } });
    await spool.flush();
    assert.equal(spool.status().failed, 1);
    assert.equal(spool.status().pending, 0);
    assert.equal(spool.records[0].payload.text, undefined);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
