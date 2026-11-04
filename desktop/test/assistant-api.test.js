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

test('AssistantApi resolves the single main session', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => ({ session: { id: 'main-1', kind: 'main' } }) };
    },
  });
  const response = await api.mainSession();
  assert.equal(request.url, 'http://127.0.0.1:8080/api/sessions/main');
  assert.equal(request.options.method, 'GET');
  assert.equal(response.session.id, 'main-1');
});

test('AssistantApi exposes workbench, brief and channel endpoints', async () => {
  const requests = [];
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      requests.push({ url, method: options.method, body: options.body });
      return { ok: true, json: async () => ({}) };
    },
  });
  await api.listWorkerTasks('thread-1');
  await api.cancelWorkerTask('task-1');
  await api.listOutbox();
  await api.approveOutbox('outbox-1', 2);
  await api.latestBrief('2026-11-01');
  await api.generateBrief('2026-11-01');
  await api.getEmailSettings();
  await api.saveEmailSettings({ enabled: false });
  await api.channelSettings();
  await api.updateChannelPolicy({ channel: 'wechat', source: '项目群', allowed: true });
  await api.captureActivityEpisode({ episode_id: 'ep-1' });
  await api.captureWeChatMessage({ source: '项目群', text: 'hi' });

  const paths = requests.map(item => `${item.method} ${item.url.replace('http://127.0.0.1:8080', '')}`);
  assert.deepEqual(paths, [
    'GET /api/worker-tasks?thread_id=thread-1',
    'POST /api/worker-tasks/task-1/cancel',
    'GET /api/action-outbox?status=pending_approval',
    'POST /api/action-outbox/outbox-1/approve',
    'GET /api/briefs/latest?date=2026-11-01',
    'POST /api/briefs/generate?date=2026-11-01',
    'GET /api/settings/email',
    'PUT /api/settings/email',
    'GET /api/settings/channels',
    'PUT /api/settings/channels',
    'POST /api/channels/activity/episodes',
    'POST /api/channels/wechat/messages',
  ]);
});

test('AssistantApi exposes WorkTask, artifact and grant endpoints', async () => {
  const requests = [];
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      requests.push({ url, method: options.method, body: options.body });
      return { ok: true, json: async () => ({}) };
    },
  });
  await api.listWorkTasks('active', 20);
  await api.getWorkTask('task-1');
  await api.createWorkTask({ title: 't', goal: 'g', start: true });
  await api.messageWorkTask('task-1', '补充', 3);
  await api.pauseWorkTask('task-1', 3);
  await api.resumeWorkTask('task-1', 4);
  await api.cancelWorkTask('task-1', 5);
  await api.recoverWorkTask('task-1', 4);
  await api.reopenWorkTask('task-1', 5);
  await api.completeWorkTask('task-1', 6);
  await api.listTaskEvents('task-1', 10);
  await api.createArtifact('task-1', { kind: 'text', title: 'a', content: 'b' });
  await api.getArtifact('artifact-1');
  await api.diffArtifact('artifact-1', 1, 2);
  await api.submitArtifact('artifact-1', 2);
  await api.acceptArtifact('artifact-1', 2);
  await api.rejectArtifact('artifact-1', 2, '改一下');
  await api.listTaskGrants('task-1');
  await api.createTaskGrant('task-1', { action_type: 'bash' });
  await api.revokeGrant('grant-1');

  const paths = requests.map(item => `${item.method} ${item.url.replace('http://127.0.0.1:8080', '')}`);
  assert.deepEqual(paths, [
    'GET /api/work-tasks?limit=20&status=active',
    'GET /api/work-tasks/task-1',
    'POST /api/work-tasks',
    'POST /api/work-tasks/task-1/messages',
    'POST /api/work-tasks/task-1/pause',
    'POST /api/work-tasks/task-1/resume',
    'POST /api/work-tasks/task-1/cancel',
    'POST /api/work-tasks/task-1/recover',
    'POST /api/work-tasks/task-1/reopen',
    'POST /api/work-tasks/task-1/complete',
    'GET /api/work-tasks/task-1/events?after_id=10',
    'POST /api/work-tasks/task-1/artifacts',
    'GET /api/artifacts/artifact-1',
    'GET /api/artifacts/artifact-1/diff?from=1&to=2',
    'POST /api/artifacts/artifact-1/submit',
    'POST /api/artifacts/artifact-1/accept',
    'POST /api/artifacts/artifact-1/reject',
    'GET /api/work-tasks/task-1/grants',
    'POST /api/work-tasks/task-1/grants',
    'DELETE /api/grants/grant-1',
  ]);
  assert.deepEqual(JSON.parse(requests[3].body), { text: '补充', expected_version: 3 });
  assert.deepEqual(JSON.parse(requests[14].body), { expected_version: 2 });
  assert.deepEqual(JSON.parse(requests[15].body), { expected_version: 2 });
  assert.deepEqual(JSON.parse(requests[16].body), { expected_version: 2, reason: '改一下' });
});

test('AssistantApi sends local work context with streamed chat', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      request = { url, options };
      const body = 'event: done\ndata: {"session_id":"session-1","reply":"好的"}\n\n';
      return {
        ok: true,
        body: new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(body));
            controller.close();
          },
        }),
      };
    },
  });
  const response = await api.chatStream('今天做了什么', 'session-0', null, undefined, [], '今天的阶段总结');
  assert.equal(request.url, 'http://127.0.0.1:8080/api/chat/stream');
  assert.deepEqual(JSON.parse(request.options.body), {
    text: '今天做了什么',
    session_id: 'session-0',
    context: '今天的阶段总结',
  });
  assert.equal(response.reply, '好的');
});

test('AssistantApi requestRaw preserves non-success status and body for the spool', async () => {
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async () => ({ ok: false, status: 202, json: async () => ({ filtered: true, reason: 'denied' }) }),
  });
  const result = await api.requestRaw('/api/channels/wechat/messages', { method: 'POST', body: '{}' });
  assert.equal(result.status, 202);
  assert.equal(result.body.filtered, true);
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

test('AssistantApi summarizes activity through the sub-agent endpoint', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => ({ reply: '今天修改了设置。' }) };
    },
  });
  const result = await api.summarizeActivity('今天的工作轨迹', '今天做了什么');
  assert.equal(request.url, 'http://127.0.0.1:8080/api/summarize');
  assert.deepEqual(JSON.parse(request.options.body), { context: '今天的工作轨迹', focus: '今天做了什么' });
  assert.equal(result.reply, '今天修改了设置。');
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

test('AssistantApi pages session messages with a before cursor', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => [{ id: 'm9', role: 'assistant', content: 'old' }] };
    },
  });
  await api.sessionMessages('s1', 'm10', 50);
  assert.equal(request.url, 'http://127.0.0.1:8080/api/sessions/s1/messages?limit=50&before=m10');
  assert.equal(request.options.method, 'GET');
});

test('AssistantApi answers an agent clarification with session and question id', async () => {
  let request;
  const api = new AssistantApi({
    baseUrl: 'http://127.0.0.1:8080',
    fetchImpl: async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => ({ state: 'completed', output: 'done' }) };
    },
  });
  await api.answerAgent('s1', 'approve', 'ask-1');
  assert.equal(request.url, 'http://127.0.0.1:8080/api/agents/clarify');
  assert.equal(request.options.method, 'POST');
  assert.deepEqual(JSON.parse(request.options.body), {
    session_id: 's1', answer: 'approve', question_id: 'ask-1',
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
