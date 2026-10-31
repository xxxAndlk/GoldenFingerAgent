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

  // 会话历史：按日期筛选（可选）与读取某个会话的消息。
  listSessions(date = '') {
    const query = date ? `?date=${encodeURIComponent(date)}` : '';
    return this.request(`/api/sessions${query}`, { method: 'GET' });
  }

  sessionMessages(id) {
    return this.request(`/api/sessions/${encodeURIComponent(id)}/messages`, { method: 'GET' });
  }

  uploadAttachment(payload) {
    return this.request('/api/uploads', {
      method: 'POST',
      body: JSON.stringify(payload || {}),
    });
  }

  // 流式对话：SSE 状态（思考/工具/澄清）+ 最终结果。
  // onStatus(ev) 收到每个 status 事件；返回解析后的最终响应。
  async chatStream(text, sessionId, onStatus, signal, attachments = []) {
    const headers = { 'Content-Type': 'application/json' };
    if (this.token) headers['X-Token'] = this.token;
    if (this.userId) headers['X-User-Id'] = this.userId;
    const body = { text, session_id: sessionId || '' };
    if (attachments.length) body.attachments = attachments;
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

  chat(text, sessionId = '', signal, internal = false, attachments = []) {
    const body = { text, session_id: sessionId };
    if (internal) body.internal = true;
    if (attachments.length) body.attachments = attachments;
    return this.request('/api/chat', {
      method: 'POST',
      body: JSON.stringify(body),
      // 对话内可能调用 CLI Agent 或最长 5 分钟的 bash 工具。
      signal: signal || AbortSignal.timeout(30 * 60_000),
    });
  }

  listAgents() {
    return this.request('/api/agents', { method: 'GET' });
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
