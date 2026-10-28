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
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.json();
  }

  status() {
    return this.request('/api/me', { method: 'GET' });
  }

  chat(text, sessionId = '') {
    return this.request('/api/chat', {
      method: 'POST',
      body: JSON.stringify({ text, session_id: sessionId }),
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
