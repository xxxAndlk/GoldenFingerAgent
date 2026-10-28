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
