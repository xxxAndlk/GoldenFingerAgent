'use strict';

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const readline = require('node:readline');

let requestCounter = 0;

function debug(message) {
  const file = process.env.GFA_WECHAT_DEBUG;
  if (!file) return;
  try { fs.appendFileSync(file, `${new Date().toISOString()} ${message}\n`); } catch { /* 诊断日志失败不影响连接器 */ }
}

function nextRequestId() {
  requestCounter += 1;
  return `req-${requestCounter}`;
}

function chatKeyFromSource(sourceId) {
  const parts = String(sourceId || '').split(':');
  return parts.length >= 3 ? parts.slice(2).join(':') : '';
}

// Electron 侧只负责监督 Go 连接器、校验来源和投递；数据库/密钥不进入模型或日志。
class WeChatDbConnector {
  constructor({
    connectorPath, workDir, statePath, spool,
    now = () => Date.now(), logger = console,
  }) {
    this.connectorPath = connectorPath;
    this.workDir = workDir;
    this.statePath = statePath;
    this.spool = spool;
    this.now = now;
    this.logger = logger;
    this.child = null;
    this.reader = null;
    this.ready = null;
    this.resolveReady = null;
    this.rejectReady = null;
    this.requests = new Map();
    this.stopping = false;
    this.restartAttempts = 0;
    this.restartTimer = null;
    this.config = { enabled: false, chats: [], intervalMs: 2000 };
    this.allowedSources = new Set();
    this.healthValue = {};
    this.lastError = '';
    this.lastEventAt = '';
    this.startedAt = 0;
    this.denied = 0;
    this.healthTimer = null;
  }

  status() {
    const spool = this.spool ? this.spool.status() : {};
    let state = this.healthValue.state || 'off';
    if (!this.child) state = this.stopping ? 'off' : (this.lastError ? 'error' : 'starting');
    else if (!this.config.enabled) state = 'off';
    else if (!this.config.chats.length) state = 'waiting_authorization';
    else if (state === 'off') state = 'observing';
    return {
      state,
      enabled: Boolean(this.config.enabled),
      authorized: Boolean(this.config.chats.length),
      authorizedChats: this.config.chats.length,
      currentSource: '',
      lastObservedAt: this.healthValue.last_db_read_at || '',
      lastEventAt: this.healthValue.last_event_at || this.lastEventAt,
      lastDeliveredAt: spool.lastDeliveredAt || '',
      completeness: state === 'observing' ? 'complete' : 'unknown',
      pending: spool.pending || 0,
      filtered: spool.filtered || 0,
      failed: spool.failed || 0,
      lastError: this.lastError || spool.lastError || '',
      denied: this.denied,
      sources: this.config.chats.map(item => item.source_id).filter(Boolean),
    };
  }

  async start() {
    if (this.child) return this.ready;
    if (!this.connectorPath) throw new Error('未找到微信数据库 Go 连接器');
    this.stopping = false;
    this.startedAt = this.now();
    const child = spawn(this.connectorPath, [
      '--workdir', this.workDir,
      '--state', this.statePath,
    ], {
      cwd: path.dirname(this.connectorPath),
      windowsHide: true,
      env: { ...process.env },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.child = child;
    debug(`spawn pid=${child.pid} go=${this.connectorPath}`);
    this.ready = new Promise((resolve, reject) => {
      this.resolveReady = resolve;
      this.rejectReady = reject;
      const timer = setTimeout(() => reject(new Error('微信数据库连接器启动超时')), 180_000);
      timer.unref?.();
      this.readyTimer = timer;
    });
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stderr.on('data', () => {
      // 第三方日志可能包含本机路径，不写入应用日志；错误通过协议返回。
    });
    this.reader = readline.createInterface({ input: child.stdout, crlfDelay: Infinity });
    this.reader.on('line', line => this.handleLine(line));
    child.on('exit', (code, signal) => this.handleExit(code, signal));
    child.on('error', error => {
      this.lastError = `微信数据库连接器启动失败：${error.message}`;
      this.rejectReady?.(error);
    });
    try {
      await this.ready;
    } catch (error) {
      this.lastError = error.message;
      throw error;
    }
    this.restartAttempts = 0;
    if (this.config.enabled || this.config.chats.length) {
      await this.sendConfigure();
    }
    return this.ready;
  }

  handleLine(line) {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      debug(`bad line ${String(line).slice(0, 80)}`);
      return;
    }
    debug(`line ${message.type || 'unknown'} ${message.request_id || ''}`);
    if (message.type === 'ready') {
      clearTimeout(this.readyTimer);
      this.resolveReady?.(message);
      return;
    }
    if (message.type === 'message' && message.message) {
      this.handleMessage(message.message).catch(error => {
        this.lastError = `微信消息投递失败：${error.message}`;
      });
      return;
    }
    if (message.type === 'health') {
      this.healthValue = message;
      if (message.request_id && this.requests.has(message.request_id)) {
        const request = this.requests.get(message.request_id);
        this.requests.delete(message.request_id);
        clearTimeout(request.timer);
        request.resolve(message);
      }
      return;
    }
    if (message.type === 'error') {
      this.lastError = String(message.message || message.code || '微信数据库连接器错误').slice(0, 300);
      if (message.request_id && this.requests.has(message.request_id)) {
        const request = this.requests.get(message.request_id);
        this.requests.delete(message.request_id);
        clearTimeout(request.timer);
        request.reject(new Error(this.lastError));
      }
      return;
    }
    const requestId = message.request_id;
    if (requestId && this.requests.has(requestId)) {
      const request = this.requests.get(requestId);
      this.requests.delete(requestId);
      clearTimeout(request.timer);
      request.resolve(message);
    }
  }

  async handleMessage(message) {
    if (Number(message.schema_version) !== 2) return;
    const sourceId = String(message.source_id || '');
    if (!sourceId || !this.allowedSources.has(sourceId)) {
      this.denied += 1;
      return;
    }
    const eventId = String(message.event_id || message.message_id || '');
    if (!eventId) throw new Error('微信消息缺少稳定 event_id');
    this.lastEventAt = new Date(this.now()).toISOString();
    this.spool.enqueue({
      endpoint: '/api/channels/wechat/messages',
      event: eventId,
      kind: 'wechat_db_message',
      sourceKey: sourceId,
      payload: message,
    });
  }

  handleExit(code, signal) {
    clearTimeout(this.readyTimer);
    this.reader?.close();
    this.reader = null;
    this.child = null;
    for (const request of this.requests.values()) {
      clearTimeout(request.timer);
      request.reject(new Error('微信数据库连接器已退出'));
    }
    this.requests.clear();
    this.rejectReady?.(new Error('微信数据库连接器已退出'));
    if (this.stopping) return;
    this.lastError = `微信数据库连接器退出（${code ?? signal ?? 'unknown'}）`;
    const delay = Math.min(30_000, 1000 * (2 ** Math.min(this.restartAttempts, 5)));
    this.restartAttempts += 1;
    if (this.restartAttempts <= 5) {
      this.restartTimer = setTimeout(() => {
        this.start().catch(error => {
          this.lastError = error.message;
        });
      }, delay);
      this.restartTimer.unref?.();
    }
  }

  async ensureStarted() {
    if (!this.child) await this.start();
  }

  send(payload, timeout = 120_000) {
    return new Promise((resolve, reject) => {
      if (!this.child) {
        reject(new Error('微信数据库连接器未启动'));
        return;
      }
      const id = payload.id || nextRequestId();
      const timer = setTimeout(() => {
        this.requests.delete(id);
        reject(new Error('微信数据库连接器请求超时'));
      }, timeout);
      timer.unref?.();
      this.requests.set(id, { resolve, reject, timer });
      debug(`send ${payload.type} ${id}`);
      this.child.stdin.write(JSON.stringify({ ...payload, id }) + '\n');
    });
  }

  async sendConfigure() {
    if (!this.child) return;
    const enabled = Boolean(this.config.enabled && this.config.chats.length);
    this.allowedSources = new Set(this.config.chats.map(item => item.source_id).filter(Boolean));
    try {
      await this.send({
        type: 'configure',
        enabled,
        chats: this.config.chats,
        interval_ms: this.config.intervalMs,
        mode: 'latest_only',
      }, 180_000);
      this.lastError = '';
    } catch (error) {
      this.lastError = error.message;
    }
  }

  async configure({ enabled, chats, intervalMs = 2000 }) {
    this.config = {
      enabled: Boolean(enabled),
      chats: (chats || []).filter(item => item && item.chat_key).map(item => ({
        chat_key: String(item.chat_key),
        source_id: String(item.source_id || ''),
        display_name: String(item.display_name || ''),
      })),
      intervalMs,
    };
    await this.ensureStarted();
    await this.sendConfigure();
    if (!this.healthTimer) {
      this.healthTimer = setInterval(() => {
        this.send({ type: 'health' }, 10_000).catch(() => {});
      }, 5000);
      this.healthTimer.unref?.();
    }
    return this.status();
  }

  async listChats(query = '', limit = 50) {
    await this.ensureStarted();
    const reply = await this.send({ type: 'list_chats', query, limit }, 180_000);
    if (reply.type !== 'chats') throw new Error(reply.message || '会话列表读取失败');
    return reply.chats || [];
  }

  async test({ chatKey = '', sourceId = '' } = {}) {
    await this.ensureStarted();
    const reply = await this.send({ type: 'test', chat_key: chatKey || chatKeyFromSource(sourceId), source_id: sourceId }, 180_000);
    if (reply.type !== 'test') throw new Error(reply.message || '数据库连接测试失败');
    return reply;
  }

  async stop() {
    this.stopping = true;
    clearTimeout(this.restartTimer);
    clearInterval(this.healthTimer);
    this.restartTimer = null;
    this.healthTimer = null;
    if (this.child) {
      try {
        this.child.stdin.write(JSON.stringify({ type: 'stop' }) + '\n');
      } catch { /* 进程可能已退出 */ }
      await new Promise(resolve => setTimeout(resolve, 300));
      try { this.child.kill(); } catch { /* 已退出 */ }
      this.child = null;
    }
  }
}

module.exports = { WeChatDbConnector, chatKeyFromSource };
