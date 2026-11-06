'use strict';

class AssistantApi {
  constructor({ baseUrl, token = '', userId = '', fetchImpl = fetch }) {
    this.baseUrl = baseUrl.replace(/\/$/, '');
    this.token = token;
    this.userId = userId;
    this.fetch = fetchImpl;
  }

  async request(path, options = {}) {
    const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
    if (this.token) headers['X-Token'] = this.token;
    if (this.userId) headers['X-User-Id'] = this.userId;
    const response = await this.fetch(`${this.baseUrl}${path}`, {
      ...options,
      headers,
      signal: options.signal || AbortSignal.timeout(65_000),
    });
    if (!response.ok) {
      let detail = '';
      try {
        const body = await response.json();
        detail = body.error || body.message || '';
      } catch {
        // 非 JSON 错误响应仍保留 HTTP 状态。
      }
      throw new Error(detail ? `HTTP ${response.status}：${detail}` : `HTTP ${response.status}`);
    }
    return response.json();
  }

  // 投递队列需要拿到原始状态码，不能用 request 的抛错路径丢失 4xx/5xx 语义。
  async requestRaw(path, options = {}) {
    const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
    if (this.token) headers['X-Token'] = this.token;
    if (this.userId) headers['X-User-Id'] = this.userId;
    const response = await this.fetch(`${this.baseUrl}${path}`, {
      ...options,
      headers,
      signal: options.signal || AbortSignal.timeout(65_000),
    });
    let body = null;
    try { body = await response.json(); } catch { /* 空响应不视为投递失败 */ }
    return { status: response.status, body };
  }

  status() {
    return this.request('/api/me', { method: 'GET' });
  }

  getLLMSettings() {
    return this.request('/api/settings/llm', { method: 'GET' });
  }

  listSkills() {
    return this.request('/api/skills', { method: 'GET' });
  }

  // 完整设置：当前模型 + 模型方案 + 搜索密钥 + 中继。
  getSettings() {
    return this.request('/api/settings', { method: 'GET' });
  }

  saveModel(model) {
    return this.request('/api/settings/models', {
      method: 'POST',
      body: JSON.stringify({
        id: model.id || '',
        name: String(model.name || '').trim(),
        base_url: String(model.base_url || '').trim(),
        model: String(model.model || '').trim(),
        api_key: model.api_key || '',
        protocol: model.protocol || 'responses',
        reasoning_effort: model.reasoning_effort || '',
        max_context: Number(model.max_context) || 0,
        max_output: Number(model.max_output) || 0,
        temperature: Number(model.temperature) || 0,
        active: Boolean(model.active),
      }),
    });
  }

  activateModel(id) {
    return this.request(`/api/settings/models/${encodeURIComponent(id)}/activate`, { method: 'POST' });
  }

  deleteModel(id) {
    return this.request(`/api/settings/models/${encodeURIComponent(id)}`, { method: 'DELETE' });
  }

  saveSearch(search) {
    return this.request('/api/settings/search', {
      method: 'PUT',
      body: JSON.stringify({
        base_url: String(search.base_url || '').trim(),
        api_key: search.api_key || '',
      }),
    });
  }

  getCloudMemory() {
    return this.request('/api/settings/cloud-memory', { method: 'GET' });
  }

  saveCloudMemory(config) {
    return this.request('/api/settings/cloud-memory', {
      method: 'PUT',
      body: JSON.stringify(config),
    });
  }

  testCloudMemory(config) {
    return this.request('/api/cloud-memory/test', {
      method: 'POST',
      body: JSON.stringify(config),
    });
  }

  cloudMemoryStatus() {
    return this.request('/api/cloud-memory/status', { method: 'GET' });
  }

  saveRelay(relay) {
    return this.request('/api/settings/relay', {
      method: 'PUT',
      body: JSON.stringify({ url: String(relay.url || '').trim() }),
    });
  }

  saveLLMSettings(settings) {
    return this.request('/api/settings/llm', {
      method: 'PUT',
      body: JSON.stringify({
        base_url: String(settings.base_url || '').trim(),
        model: String(settings.model || '').trim(),
        api_key: settings.api_key || '',
        protocol: settings.protocol || 'responses',
        reasoning_effort: settings.reasoning_effort || '',
        max_context: Number(settings.max_context) || 0,
        max_output: Number(settings.max_output) || 0,
        temperature: Number(settings.temperature) || 0,
      }),
    });
  }

  mainSession() {
    return this.request('/api/sessions/main', { method: 'GET' });
  }

  // 会话历史：按日期筛选（可选）与读取某个会话的消息。
  listSessions(date = '') {
    const query = date ? `?date=${encodeURIComponent(date)}` : '';
    return this.request(`/api/sessions${query}`, { method: 'GET' });
  }

  sessionMessages(id, before = '', limit = 50) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (before) query.set('before', before);
    return this.request(`/api/sessions/${encodeURIComponent(id)}/messages?${query.toString()}`, { method: 'GET' });
  }

  // 统一会话目录：主对话、Worker 会话与委派执行记录。
  listConversations(kind = '', limit = 100) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (kind) query.set('kind', kind);
    return this.request(`/api/conversations?${query.toString()}`, { method: 'GET' });
  }

  conversationMessages(id, before = '', limit = 50) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (before) query.set('before', before);
    return this.request(`/api/conversations/${encodeURIComponent(id)}/messages?${query.toString()}`, { method: 'GET' });
  }

  listWorkTasks(status = '', limit = 100) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (status) query.set('status', status);
    return this.request(`/api/work-tasks?${query.toString()}`, { method: 'GET' });
  }

  getWorkTask(id) {
    return this.request(`/api/work-tasks/${encodeURIComponent(id)}`, { method: 'GET' });
  }

  createWorkTask(task) {
    return this.request('/api/work-tasks', {
      method: 'POST',
      body: JSON.stringify(task || {}),
    });
  }

  messageWorkTask(id, text, expectedVersion) {
    const body = { text: String(text || '') };
    if (Number.isInteger(expectedVersion)) body.expected_version = expectedVersion;
    return this.request(`/api/work-tasks/${encodeURIComponent(id)}/messages`, {
      method: 'POST',
      body: JSON.stringify(body),
    });
  }

  taskAction(id, action, expectedVersion) {
    const body = {};
    if (Number.isInteger(expectedVersion)) body.expected_version = expectedVersion;
    return this.request(`/api/work-tasks/${encodeURIComponent(id)}/${action}`, {
      method: 'POST',
      body: JSON.stringify(body),
    });
  }

  pauseWorkTask(id, version) { return this.taskAction(id, 'pause', version); }
  resumeWorkTask(id, version) { return this.taskAction(id, 'resume', version); }
  cancelWorkTask(id, version) { return this.taskAction(id, 'cancel', version); }
  recoverWorkTask(id, version) { return this.taskAction(id, 'recover', version); }
  reopenWorkTask(id, version) { return this.taskAction(id, 'reopen', version); }
  completeWorkTask(id, version) { return this.taskAction(id, 'complete', version); }

  listTaskEvents(id, afterId = 0) {
    const query = new URLSearchParams({ after_id: String(afterId || 0) });
    return this.request(`/api/work-tasks/${encodeURIComponent(id)}/events?${query.toString()}`, { method: 'GET' });
  }

  createArtifact(taskId, artifact) {
    return this.request(`/api/work-tasks/${encodeURIComponent(taskId)}/artifacts`, {
      method: 'POST',
      body: JSON.stringify(artifact || {}),
    });
  }

  getArtifact(id) {
    return this.request(`/api/artifacts/${encodeURIComponent(id)}`, { method: 'GET' });
  }

  diffArtifact(id, from, to) {
    const query = new URLSearchParams({ from: String(from), to: String(to) });
    return this.request(`/api/artifacts/${encodeURIComponent(id)}/diff?${query.toString()}`, { method: 'GET' });
  }

  submitArtifact(id, version) {
    return this.request(`/api/artifacts/${encodeURIComponent(id)}/submit`, {
      method: 'POST',
      body: JSON.stringify({ expected_version: Number(version) || 0 }),
    });
  }

  acceptArtifact(id, version) {
    return this.request(`/api/artifacts/${encodeURIComponent(id)}/accept`, {
      method: 'POST',
      body: JSON.stringify({ expected_version: Number(version) || 0 }),
    });
  }

  rejectArtifact(id, version, reason = '') {
    return this.request(`/api/artifacts/${encodeURIComponent(id)}/reject`, {
      method: 'POST',
      body: JSON.stringify({ expected_version: Number(version) || 0, reason }),
    });
  }

  listTaskGrants(id) {
    return this.request(`/api/work-tasks/${encodeURIComponent(id)}/grants`, { method: 'GET' });
  }

  createTaskGrant(id, grant) {
    return this.request(`/api/work-tasks/${encodeURIComponent(id)}/grants`, {
      method: 'POST',
      body: JSON.stringify(grant || {}),
    });
  }

  revokeGrant(id) {
    return this.request(`/api/grants/${encodeURIComponent(id)}`, { method: 'DELETE' });
  }

  listWorkerTasks(threadId = '') {
    const query = threadId ? `?thread_id=${encodeURIComponent(threadId)}` : '';
    return this.request(`/api/worker-tasks${query}`, { method: 'GET' });
  }

  cancelWorkerTask(id) {
    return this.request(`/api/worker-tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST' });
  }

  steerWorkerTask(id, text) {
    return this.request(`/api/worker-tasks/${encodeURIComponent(id)}/steer`, {
      method: 'POST',
      body: JSON.stringify({ text }),
    });
  }

  listOutbox(status = 'pending_approval') {
    const query = status ? `?status=${encodeURIComponent(status)}` : '';
    return this.request(`/api/action-outbox${query}`, { method: 'GET' });
  }

  approveOutbox(id, version) {
    return this.request(`/api/action-outbox/${encodeURIComponent(id)}/approve`, {
      method: 'POST',
      body: JSON.stringify({ version: Number(version) || 0 }),
    });
  }

  rejectOutbox(id, version, reason = '') {
    return this.request(`/api/action-outbox/${encodeURIComponent(id)}/reject`, {
      method: 'POST',
      body: JSON.stringify({ version: Number(version) || 0, reason }),
    });
  }

  latestBrief(date = '') {
    const query = date ? `?date=${encodeURIComponent(date)}` : '';
    return this.request(`/api/briefs/latest${query}`, { method: 'GET' });
  }

  listBriefs(limit = 30) {
    return this.request(`/api/briefs?limit=${encodeURIComponent(limit)}`, { method: 'GET' });
  }

  getBrief(id) {
    return this.request(`/api/briefs/${encodeURIComponent(id)}`, { method: 'GET' });
  }

  generateBrief(date = '') {
    const query = date ? `?date=${encodeURIComponent(date)}` : '';
    return this.request(`/api/briefs/generate${query}`, { method: 'POST', body: '{}' });
  }

  markBriefDelivered(id) {
    return this.request(`/api/briefs/${encodeURIComponent(id)}/delivered`, {
      method: 'POST',
      body: '{}',
    });
  }

  channelSettings() {
    return this.request('/api/settings/channels', { method: 'GET' });
  }

  reportConnectorStatus(channel, state, payload) {
    return this.request('/api/channels/connector/status', {
      method: 'POST',
      body: JSON.stringify({ channel, state, payload: payload || {} }),
    });
  }

  connectorStatus(channel) {
    const query = channel ? `?channel=${encodeURIComponent(channel)}` : '';
    return this.request(`/api/channels/connector/status${query}`, { method: 'GET' });
  }

  pendingConnectorRequests(channel = '') {
    const query = channel ? `?channel=${encodeURIComponent(channel)}` : '';
    return this.request(`/api/channels/connector/requests${query}`, { method: 'GET' });
  }

  completeConnectorRequest(id, result) {
    return this.request(`/api/channels/connector/requests/${encodeURIComponent(id)}/complete`, {
      method: 'POST', body: JSON.stringify(result || {}),
    });
  }

  updateChannelPolicy(policy) {
    return this.request('/api/settings/channels', {
      method: 'PUT',
      body: JSON.stringify(policy || {}),
    });
  }

  updateChannelConfig(channel, config) {
    return this.request(`/api/settings/channels/config/${encodeURIComponent(channel)}`, {
      method: 'PUT',
      body: JSON.stringify(config || {}),
    });
  }

  updateGlobalConfig(config) {
    return this.request('/api/settings/channels/global', {
      method: 'PUT',
      body: JSON.stringify(config || {}),
    });
  }

  webSites() {
    return this.request('/api/settings/web/sites', { method: 'GET' });
  }

  saveWebSite(site) {
    return this.request(`/api/settings/web/sites/${encodeURIComponent(site.id || '')}`, {
      method: 'PUT',
      body: JSON.stringify(site || {}),
    });
  }

  deleteWebSite(id) {
    return this.request(`/api/settings/web/sites/${encodeURIComponent(id)}`, { method: 'DELETE' });
  }

  captureWebItem(item) {
    return this.request('/api/channels/web/items', {
      method: 'POST',
      body: JSON.stringify(item || {}),
    });
  }

  getEmailSettings() {
    return this.request('/api/settings/email', { method: 'GET' });
  }

  saveEmailSettings(email) {
    return this.request('/api/settings/email', {
      method: 'PUT',
      body: JSON.stringify(email || {}),
    });
  }

  captureActivityEpisode(payload) {
    return this.request('/api/channels/activity/episodes', {
      method: 'POST',
      body: JSON.stringify(payload || {}),
    });
  }

  captureWeChatMessage(payload) {
    return this.request('/api/channels/wechat/messages', {
      method: 'POST',
      body: JSON.stringify(payload || {}),
    });
  }

  captureEmailMessage(payload) {
    return this.request('/api/channels/email/messages', {
      method: 'POST',
      body: JSON.stringify(payload || {}),
    });
  }

  uploadAttachment(payload) {
    return this.request('/api/uploads', {
      method: 'POST',
      body: JSON.stringify(payload || {}),
    });
  }

  // 流式对话：SSE 状态（思考/工具/澄清）+ 最终结果。
  // onStatus(ev) 收到每个 status 事件；返回解析后的最终响应。
  async chatStream(text, sessionId, onStatus, signal, attachments = [], context = '') {
    const headers = { 'Content-Type': 'application/json' };
    if (this.token) headers['X-Token'] = this.token;
    if (this.userId) headers['X-User-Id'] = this.userId;
    const body = { text, session_id: sessionId || '' };
    if (attachments.length) body.attachments = attachments;
    if (context) body.context = context;
    const response = await this.fetch(`${this.baseUrl}/api/chat/stream`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
      signal: signal || AbortSignal.timeout(30 * 60_000),
    });
    if (!response.ok || !response.body) {
      let detail = '';
      try {
        const body = await response.json();
        detail = body.error || body.message || '';
      } catch {
        // 非 JSON 错误响应只保留状态码。
      }
      throw new Error(detail ? `HTTP ${response.status}：${detail}` : `HTTP ${response.status}`);
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let final = null;
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let index;
      while ((index = buffer.indexOf('\n\n')) >= 0) {
        const chunk = buffer.slice(0, index);
        buffer = buffer.slice(index + 2);
        let event = 'message';
        let data = '';
        for (const line of chunk.split('\n')) {
          if (line.startsWith('event:')) event = line.slice(6).trim();
          else if (line.startsWith('data:')) data += line.slice(5).trim();
        }
        if (!data) continue;
        let payload;
        try {
          payload = JSON.parse(data);
        } catch {
          continue;
        }
        if (event === 'status' && typeof onStatus === 'function') onStatus(payload);
        else if (event === 'done') final = payload;
        else if (event === 'error') throw new Error(payload.error || '对话失败');
      }
    }
    if (!final) throw new Error('对话未返回结果');
    return final;
  }

  relayStatus() {
    return this.request('/api/relay/status', { method: 'GET' });
  }

  relayPairCode() {
    return this.request('/api/relay/pair-code', { method: 'POST' });
  }

  relayChannels() {
    return this.request('/api/relay/channels', { method: 'GET' });
  }

  revokeRelayChannel(channelId) {
    return this.request(`/api/relay/channels/${encodeURIComponent(channelId)}`, { method: 'DELETE' });
  }

  chat(text, sessionId = '', signal, internal = false, attachments = [], context = '') {
    const body = { text, session_id: sessionId };
    if (internal) body.internal = true;
    if (attachments.length) body.attachments = attachments;
    if (context) body.context = context;
    return this.request('/api/chat', {
      method: 'POST',
      body: JSON.stringify(body),
      // 对话内可能调用 CLI Agent 或最长 5 分钟的 bash 工具。
      signal: signal || AbortSignal.timeout(30 * 60_000),
    });
  }

  summarizeActivity(context, focus = '', signal) {
    return this.request('/api/summarize', {
      method: 'POST',
      body: JSON.stringify({ context, focus }),
      signal: signal || AbortSignal.timeout(60_000),
    });
  }

  listAgents(refresh = false) {
    return this.request(refresh ? '/api/agents?refresh=1' : '/api/agents', { method: 'GET' });
  }

  openAgent(agentId) {
    return this.request('/api/agents/open', {
      method: 'POST',
      body: JSON.stringify({ agent_id: agentId }),
    });
  }

  delegateAgent(agentId, task, workDir = '') {
    return this.request('/api/agents/delegate', {
      method: 'POST',
      body: JSON.stringify({ agent_id: agentId, task, work_dir: workDir }),
      signal: AbortSignal.timeout(30 * 60_000),
    });
  }

  answerAgent(sessionId, answer, questionId = '') {
    return this.request('/api/agents/clarify', {
      method: 'POST',
      body: JSON.stringify({ session_id: sessionId, answer, question_id: questionId }),
      signal: AbortSignal.timeout(30 * 60_000),
    });
  }

  registerA2A(endpoint) {
    return this.request('/api/agents/a2a', {
      method: 'POST',
      body: JSON.stringify(endpoint),
    });
  }

  agentProviders(appType = '') {
    return this.request(`/api/agents/providers?app_type=${encodeURIComponent(appType)}`, { method: 'GET' });
  }

  switchAgentProvider(appType, providerId) {
    return this.request('/api/agents/switch', {
      method: 'POST',
      body: JSON.stringify({ app_type: appType, provider_id: providerId, confirmed: true }),
    });
  }

  capture(text, contextBefore, contextAfter, source, capturedAt = new Date()) {
    return this.request('/api/memory/capture', {
      method: 'POST',
      body: JSON.stringify({
        text,
        context_before: contextBefore,
        context_after: contextAfter,
        source,
        captured_at: capturedAt.toISOString(),
      }),
    });
  }
}

module.exports = { AssistantApi };
