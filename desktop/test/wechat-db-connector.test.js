'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { ObservationSpool } = require('../observation-spool');
const { WeChatDbConnector } = require('../wechat-db-connector');

function tempDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-wechat-'));
}

function message(overrides = {}) {
  return {
    schema_version: 2,
    source_id: 'wechat-db:acct:wxid_contact',
    event_id: 'evt-1',
    message_id: 'acct:wxid_contact:message__message_0.db:7',
    display_name: '张三',
    actor_role: 'contact',
    direction: 'other',
    message_type: 'text',
    text: '周五评审',
    ...overrides,
  };
}

function makeConnector(spool, dir) {
  const connector = new WeChatDbConnector({
    connectorPath: path.join(dir, 'wechat-db-connector.exe'),
    workDir: path.join(dir, 'work'),
    statePath: path.join(dir, 'state.json'),
    spool,
  });
  const sent = [];
  connector.send = async payload => { sent.push(payload); return { type: 'ack' }; };
  connector.sendConfigure = async () => {};
  connector.start = async () => {};
  connector.allowedSources = new Set(['wechat-db:acct:wxid_contact']);
  return { connector, sent };
}

test('supervisor confirms a message only after the queue persisted it', async () => {
  const dir = tempDir();
  try {
    const file = path.join(dir, 'spool.json');
    const spool = new ObservationSpool({ filePath: file });
    const { connector, sent } = makeConnector(spool, dir);
    await connector.handleMessage(message());
    assert.equal(sent.length, 1);
    assert.equal(sent[0].type, 'ack');
    assert.equal(sent[0].message_id, 'acct:wxid_contact:message__message_0.db:7');
    assert.equal(sent[0].ok, true);
    const saved = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.equal(saved.length, 1);
    assert.equal(saved[0].payload.text, '周五评审');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('supervisor refuses to confirm when the queue cannot be persisted', async () => {
  const dir = tempDir();
  try {
    // 目标是目录，落盘必然失败：这条消息不能算已入库。
    const blocked = path.join(dir, 'blocked');
    fs.mkdirSync(blocked);
    const spool = new ObservationSpool({ filePath: blocked });
    const { connector, sent } = makeConnector(spool, dir);
    await connector.handleMessage(message());
    assert.equal(sent.length, 1);
    assert.equal(sent[0].ok, false);
    assert.equal(sent[0].discard, false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('supervisor does not confirm messages of unauthorized sources', async () => {
  const dir = tempDir();
  try {
    const spool = new ObservationSpool({ filePath: path.join(dir, 'spool.json') });
    const { connector, sent } = makeConnector(spool, dir);
    await connector.handleMessage(message({ source_id: 'wechat-db:acct:other' }));
    assert.equal(sent.length, 1);
    assert.equal(sent[0].ok, false);
    assert.equal(sent[0].discard, true);
    assert.equal(spool.records.length, 0);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('revoking a source clears queued message bodies', async () => {
  const dir = tempDir();
  try {
    const spool = new ObservationSpool({ filePath: path.join(dir, 'spool.json') });
    spool.enqueue({
      endpoint: '/api/channels/wechat/messages',
      payload: { event_id: 'evt-1', text: '机密内容' },
      sourceKey: 'wechat-db:acct:wxid_contact',
    });
    const { connector } = makeConnector(spool, dir);
    connector.config = {
      enabled: true,
      chats: [{ chat_key: 'wxid_contact', source_id: 'wechat-db:acct:wxid_contact' }],
      intervalMs: 2000,
    };
    await connector.configure({ enabled: false, chats: [] });
    const record = spool.records[0];
    assert.equal(record.status, 'filtered');
    assert.equal(record.payload.dropped, true);
    assert.equal(record.payload.text, undefined);
    assert.equal(record.sourceKey, '');
    assert.equal(connector.status().revoked, 1);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
