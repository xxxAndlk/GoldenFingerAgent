'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { AssistantApi } = require('../assistant-api');

test('AssistantApi sends chat session and local service identity headers', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080/',
    token: 'test-token',
    userId: 'user-1',
    fetchImpl: async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => ({ session_id: 'session-1', reply: '好的' }) };
    },
  });
  const response = await api.chat('帮我记一下', 'session-0');
  assert.equal(request.url, 'http://127.0.0.1:8080/api/chat');
  assert.equal(request.options.headers['X-Token'], 'test-token');
  assert.equal(request.options.headers['X-User-Id'], 'user-1');
  assert.deepEqual(JSON.parse(request.options.body), { text: '帮我记一下', session_id: 'session-0' });
  assert.equal(response.reply, '好的');
});

test('AssistantApi rejects non-success responses', async () => {
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async () => ({ ok: false, status: 503 }),
  });
  await assert.rejects(() => api.status(), /HTTP 503/);
});

test('AssistantApi reads and saves LLM settings', async () => {
  const requests = [];
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      requests.push({ url, options });
      return { ok: true, json: async () => ({ base_url: 'https://example.com/v1', model: 'model-1', api_key: '****1234', has_key: true }) };
    },
  });
  await api.getLLMSettings();
  await api.saveLLMSettings({ base_url: ' https://example.com/v1 ', model: ' model-1 ', api_key: '****1234' });
  assert.equal(requests[0].url, 'http://127.0.0.1:8080/api/settings/llm');
  assert.equal(requests[0].options.method, 'GET');
  assert.equal(requests[1].url, 'http://127.0.0.1:8080/api/settings/llm');
  assert.equal(requests[1].options.method, 'PUT');
  assert.deepEqual(JSON.parse(requests[1].options.body), {
    base_url: 'https://example.com/v1', model: 'model-1', api_key: '****1234', protocol: 'responses',
    // 生成参数随模型一起保存；未填写时为 0/空，由服务端回落默认值。
    reasoning_effort: '', max_context: 0, max_output: 0, temperature: 0,
  });
});

test('AssistantApi opens an installed desktop agent by id', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => ({ agent_id: 'qoder', state: 'opened' }) };
    },
  });
  const result = await api.openAgent('qoder');
  assert.equal(request.url, 'http://127.0.0.1:8080/api/agents/open');
  assert.equal(request.options.method, 'POST');
  assert.deepEqual(JSON.parse(request.options.body), { agent_id: 'qoder' });
  assert.equal(result.state, 'opened');
});

test('AssistantApi delegates with an optional project directory', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => ({ state: 'completed', output: 'done' }) };
    },
  });
  await api.delegateAgent('bma', '修复测试', 'D:\\project');
  assert.equal(request.url, 'http://127.0.0.1:8080/api/agents/delegate');
  assert.deepEqual(JSON.parse(request.options.body), {
    agent_id: 'bma', task: '修复测试', work_dir: 'D:\\project',
  });
});

test('AssistantApi exposes service error details', async () => {
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async () => ({ ok: false, status: 400, json: async () => ({ error: 'base_url and model are required' }) }),
  });
  await assert.rejects(() => api.getLLMSettings(), /HTTP 400：base_url and model are required/);
});

test('AssistantApi exposes relay pairing endpoints', async () => {
  const requests = [];
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      requests.push({ url, method: options.method });
      if (url.endsWith('/api/relay/channels')) return { ok: true, json: async () => ({ channels: [{ channel_id: 'ch-1', phone_name: '手机' }] }) };
      if (url.endsWith('/api/relay/pair-code')) return { ok: true, json: async () => ({ code: '123456', expires_at: '2026-10-30T12:00:00Z' }) };
      return { ok: true, json: async () => ({ configured: true, connected: true }) };
    },
  });
  const status = await api.relayStatus();
  assert.equal(status.connected, true);
  const code = await api.relayPairCode();
  assert.equal(code.code, '123456');
  const channels = await api.relayChannels();
  assert.equal(channels.channels[0].channel_id, 'ch-1');
  await api.revokeRelayChannel('ch-1');
  assert.deepEqual(requests.map(r => `${r.method} ${r.url.split('8080')[1]}`), [
    'GET /api/relay/status',
    'POST /api/relay/pair-code',
    'GET /api/relay/channels',
    'DELETE /api/relay/channels/ch-1',
  ]);
});
