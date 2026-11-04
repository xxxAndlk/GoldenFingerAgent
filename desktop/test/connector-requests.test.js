'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { ConnectorRequestRunner, safeWeChatChats } = require('../connector-requests');

test('wechat discovery returns safe fields only', () => {
  const chats = safeWeChatChats([{
    source_id: 'wechat-db:acct:wxid_one', chat_key: 'wxid_one', display_name: '张三',
    chat_type: 'private', account_id: 'acct', last_message_at: '2026-11-04T12:00:00+08:00',
    summary: '不应回传的摘要', text: '不应回传的正文', db_path: 'C:\\secret\\message.db', key: 'secret',
  }]);
  assert.equal(chats.length, 1);
  assert.deepEqual(Object.keys(chats[0]).sort(), ['account_id', 'chat_key', 'chat_type', 'display_name', 'last_message_at', 'source_id']);
  assert.equal(JSON.stringify(chats).includes('不应回传'), false);
  assert.equal(JSON.stringify(chats).includes('secret'), false);
});

test('wechat request refreshes channel configuration before discovery', async () => {
  const calls = [];
  const runner = new ConnectorRequestRunner({
    api: {},
    refreshChannels: async () => { calls.push('refresh'); },
    listWeChatChats: async (query, limit) => {
      calls.push(`list:${query}:${limit}`);
      return [{ source_id: 'wechat-db:acct:one', chat_key: 'one', display_name: '张三' }];
    },
    setActivityEnabled: async () => { throw new Error('不应调用'); },
  });
  const result = await runner.execute({ id: 'r1', channel: 'wechat_desktop', kind: 'list_wechat_chats', query: '张', limit: 20 });
  assert.equal(result.ok, true);
  assert.equal(result.chats.length, 1);
  assert.deepEqual(calls, ['refresh', 'list:张:20']);
});

test('activity request returns the real desktop switch state', async () => {
  const runner = new ConnectorRequestRunner({
    api: {}, refreshChannels: async () => {}, listWeChatChats: async () => [],
    setActivityEnabled: async enabled => ({ enabled }),
  });
  assert.deepEqual(await runner.execute({ id: 'r2', channel: 'activity', kind: 'set_activity', enabled: true }), { ok: true, enabled: true });
  assert.deepEqual(await runner.execute({ id: 'r3', channel: 'activity', kind: 'set_activity', enabled: false }), { ok: true, enabled: false });
});

test('runner reports safe execution errors', async () => {
  const runner = new ConnectorRequestRunner({
    api: {}, refreshChannels: async () => { throw new Error('读取失败\nC:\\private\\db'); },
    listWeChatChats: async () => [], setActivityEnabled: async () => ({}),
  });
  const result = await runner.execute({ id: 'r4', channel: 'wechat_desktop', kind: 'list_wechat_chats' });
  assert.equal(result.ok, false);
  assert.equal(result.error.includes('\n'), false);
  assert.ok(result.error.length <= 300);
});

test('polling cannot reenter while a connector request is running', async () => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  let completed = 0;
  let refreshed = 0;
  const runner = new ConnectorRequestRunner({
    api: {
      pendingConnectorRequests: async () => ({ requests: [{ id: 'r5', channel: 'activity', kind: 'set_activity', enabled: true }] }),
      completeConnectorRequest: async () => { completed += 1; },
    },
    refreshChannels: async () => { refreshed += 1; }, listWeChatChats: async () => [],
    setActivityEnabled: async enabled => { await gate; return { enabled }; },
  });
  const first = runner.pollOnce();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(await runner.pollOnce(), false);
  release();
  assert.equal(await first, true);
  assert.equal(completed, 1);
  assert.equal(refreshed, 1);
});
