'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { captureWindow } = require('./window-capture');
const { readScreenOcr } = require('./screen-ocr');
const { readWeChatWindow } = require('./wechat-window');
const { parseWeChatChat, alignVisibleMessages } = require('./wechat-parser');
const { buildEnvelope, messageKey } = require('./observation-envelope');

const DEFAULT_INTERVAL = 3500;
const MAX_STATE_MESSAGES = 120;
const ENDPOINT = '/api/channels/wechat/messages';

function readPngSize(file) {
  const buffer = fs.readFileSync(file);
  if (buffer.length < 24 || buffer.readUInt32BE(0) !== 0x89504e47) return null;
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

function readJSON(file) {
  try {
    const value = JSON.parse(fs.readFileSync(file, 'utf8'));
    return value && typeof value === 'object' ? value : {};
  } catch {
    return {};
  }
}

function writeJSON(file, value) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const temp = `${file}.tmp`;
  fs.writeFileSync(temp, JSON.stringify(value), 'utf8');
  fs.renameSync(temp, file);
}

function iso(value) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? new Date(number).toISOString() : '';
}

// 独立微信连接器：只读取当前打开会话的可见屏幕，不读数据库、不注入、不遍历会话。
class WeChatConnector {
  constructor({
    statePath,
    captureDir,
    spool,
    isEnabled = async () => false,
    isSourceAllowed = async () => false,
    readWindow = readWeChatWindow,
    capture = captureWindow,
    ocr = readScreenOcr,
    now = () => Date.now(),
    intervalMs = DEFAULT_INTERVAL,
    logger = console,
  }) {
    this.statePath = statePath;
    this.captureDir = captureDir;
    this.spool = spool;
    this.isEnabled = isEnabled;
    this.isSourceAllowed = isSourceAllowed;
    this.readWindow = readWindow;
    this.capture = capture;
    this.ocr = ocr;
    this.now = now;
    this.intervalMs = intervalMs;
    this.logger = logger;
    this.timer = null;
    this.busy = false;
    this.state = { sources: {} };
    this.statusValue = {
      state: 'off', enabled: false, authorized: false, currentSource: '',
      lastObservedAt: '', lastEventAt: '', completeness: 'unknown',
      lastError: '', gap: false, skipped: 0,
    };
    try { this.state = readJSON(statePath); } catch { this.state = {}; }
    if (!this.state.sources || typeof this.state.sources !== 'object') this.state.sources = {};
  }

  setStatus(patch) {
    this.statusValue = { ...this.statusValue, ...patch };
  }

  sourceState(sourceKey) {
    const source = this.state.sources[sourceKey];
    return source && Array.isArray(source.lastMessages) ? source : null;
  }

  persistState() {
    const sources = this.state.sources || {};
    for (const key of Object.keys(sources)) {
      if (Array.isArray(sources[key].lastMessages)) {
        sources[key].lastMessages = sources[key].lastMessages.slice(-MAX_STATE_MESSAGES);
      }
      const at = Number(sources[key].updatedAt || 0);
      if (at && this.now() - at > 30 * 24 * 60 * 60 * 1000) delete sources[key];
    }
    try { writeJSON(this.statePath, this.state); } catch (error) {
      this.setStatus({ lastError: `微信检查点保存失败：${error.message}` });
    }
  }

  recordSource(sourceKey, displayName, messages) {
    this.state.sources[sourceKey] = {
      displayName,
      updatedAt: this.now(),
      lastMessages: messages.map(message => ({
        key: message.key || messageKey(message.text, message.direction),
        text: message.text,
        direction: message.direction,
        event_id: message.event_id || '',
      })),
    };
    this.persistState();
  }

  markGap(sourceKey, displayName, reason) {
    const source = this.state.sources[sourceKey] || { displayName, lastMessages: [] };
    source.gapAt = this.now();
    source.gapReason = String(reason || '').slice(0, 300);
    this.state.sources[sourceKey] = source;
    this.persistState();
  }

  async processCapture(capture, { test = false } = {}) {
    const window = await this.readWindow();
    if (!window) {
      this.setStatus({ state: 'waiting_window', enabled: true, lastError: '没有检测到微信窗口' });
      return { ok: false, stage: 'window', error: '没有检测到微信窗口' };
    }
    let imagePath = '';
    try {
      imagePath = await this.capture({
        title: window.title || '微信',
        outputDir: this.captureDir,
        at: this.now(),
      });
      if (!imagePath) throw new Error('微信窗口截图为空');
      const size = readPngSize(imagePath);
      if (!size) throw new Error('无法读取微信窗口截图尺寸');
      const ocr = await this.ocr(imagePath);
      if (ocr.error) throw new Error(`OCR 失败：${ocr.error}`);
      const parsed = parseWeChatChat({ boxes: ocr.boxes || [], width: size.width, height: size.height, now: new Date(this.now()) });
      if (!parsed.title) {
        const hasMessages = parsed.messages.length > 0;
        const reason = hasMessages
          ? '无法从当前会话顶部识别会话名称'
          : '微信已打开，但当前没有打开任何会话';
        this.setStatus({
          state: hasMessages ? 'degraded' : 'waiting_conversation',
          currentSource: '', completeness: parsed.completeness,
          lastObservedAt: iso(this.now()), lastError: reason,
        });
        return { ok: false, stage: hasMessages ? 'parse' : 'waiting_conversation', error: reason, parsed };
      }
      const enabled = await this.isEnabled();
      const authorized = await this.isSourceAllowed(parsed.title);
      const preview = {
        title: parsed.title,
        sourceId: parsed.source_id,
        isGroup: parsed.is_group,
        completeness: parsed.completeness,
        messageCount: parsed.messages.length,
        messages: parsed.messages.map(message => ({
          direction: message.direction,
          sender: message.sender,
          text: buildEnvelope({ channel: 'wechat_desktop', kind: 'message', text: message.text }).text,
          observedAt: message.observed_at,
        })),
      };
      if (!enabled) {
        this.setStatus({
          state: 'off', enabled: false, authorized, currentSource: parsed.title,
          completeness: parsed.completeness, lastObservedAt: iso(this.now()), lastError: '微信渠道未启用',
        });
        return { ok: false, stage: 'authorization', error: '微信渠道未启用', ...preview, enabled, authorized };
      }
      if (!authorized) {
        this.setStatus({
          state: 'waiting_authorization', enabled: true, authorized: false, currentSource: parsed.title,
          completeness: parsed.completeness, lastObservedAt: iso(this.now()),
          lastError: `会话「${parsed.title}」未授权`,
        });
        return { ok: false, stage: 'authorization', error: `会话「${parsed.title}」未授权`, ...preview, enabled, authorized };
      }
      if (test) {
        this.setStatus({
          state: 'observing', enabled: true, authorized: true, currentSource: parsed.title,
          completeness: parsed.completeness, lastObservedAt: iso(this.now()), lastError: '',
        });
        return { ok: true, stage: 'ok', ...preview, enabled, authorized };
      }

      const sourceKey = parsed.source_id || `wechat-uia:${parsed.title}`;
      const previous = this.sourceState(sourceKey);
      const alignment = alignVisibleMessages(previous ? previous.lastMessages : [], parsed.messages);
      if (alignment.gap) {
        this.recordSource(sourceKey, parsed.title, alignment.messages);
        this.markGap(sourceKey, parsed.title, '当前可见消息与上次无法可靠对齐');
        this.setStatus({
          state: 'degraded', enabled: true, authorized: true, currentSource: parsed.title,
          completeness: 'partial', lastObservedAt: iso(this.now()), gap: true,
          lastError: '当前可见消息与上次无法可靠对齐，已保留缺口，未批量重复导入',
        });
        return { ok: true, stage: 'gap', ...preview, enqueued: 0, gap: true };
      }

      const fresh = alignment.messages.slice(alignment.overlap);
      let enqueued = 0;
      for (const message of fresh) {
        const envelope = buildEnvelope({
          channel: 'wechat_desktop',
          kind: 'message',
          event_id: message.event_id,
          source: { connector_id: 'wechat-uia', source_id: sourceKey, display_name: parsed.title },
          occurred_at: null,
          observed_at: message.observed_at,
          completeness: 'current_visible',
          title: parsed.title,
          text: message.text,
          change: { type: 'new' },
          cursor: String(message.sequence),
        });
        this.spool.enqueue({
          endpoint: ENDPOINT,
          event: envelope.event_id,
          kind: 'wechat_message',
          sourceKey,
          payload: {
            source: parsed.title,
            source_id: sourceKey,
            display_name: parsed.title,
            message_id: envelope.event_id,
            event_id: envelope.event_id,
            sender: message.sender,
            direction: message.direction,
            message_type: message.message_type,
            text: envelope.text,
            kind: 'message',
            observed_at: envelope.observed_at,
            occurred_at: null,
            received_at: envelope.observed_at,
            content_hash: envelope.content_hash,
            completeness: envelope.completeness,
            incomplete: true,
            raw_ref: '',
          },
        });
        enqueued += 1;
      }
      if (!previous || fresh.length > 0 || alignment.overlap !== alignment.messages.length) {
        this.recordSource(sourceKey, parsed.title, alignment.messages);
      }
      this.setStatus({
        state: 'observing', enabled: true, authorized: true, currentSource: parsed.title,
        completeness: parsed.completeness, lastObservedAt: iso(this.now()),
        lastEventAt: enqueued ? iso(this.now()) : this.statusValue.lastEventAt,
        gap: false, lastError: '',
      });
      return { ok: true, stage: enqueued ? 'ingested' : 'unchanged', ...preview, enqueued, gap: false };
    } catch (error) {
      this.setStatus({ state: 'error', enabled: true, lastError: String(error.message || error) });
      return { ok: false, stage: 'error', error: String(error.message || error) };
    } finally {
      if (imagePath) {
        try { fs.rmSync(imagePath, { force: true }); } catch { /* 临时截图清理失败不阻塞下一轮 */ }
      }
    }
  }

  async poll() {
    if (this.busy) return this.status();
    const enabled = await this.isEnabled();
    if (!enabled) {
      this.setStatus({ state: 'off', enabled: false, authorized: false, lastError: '微信渠道未启用' });
      return this.status();
    }
    this.busy = true;
    try {
      await this.processCapture();
    } finally {
      this.busy = false;
    }
    return this.status();
  }

  async testCapture() {
    if (this.busy) throw new Error('微信采集正在运行，请稍后再试');
    this.busy = true;
    try {
      return await this.processCapture(null, { test: true });
    } finally {
      this.busy = false;
    }
  }

  start() {
    if (this.timer) return this.status();
    this.timer = setInterval(() => {
      this.poll().catch(error => this.setStatus({ state: 'error', lastError: String(error.message || error) }));
    }, this.intervalMs);
    this.timer.unref?.();
    this.poll().catch(() => {});
    return this.status();
  }

  stop() {
    clearInterval(this.timer);
    this.timer = null;
    this.setStatus({ state: 'off' });
    return this.status();
  }

  status() {
    const spool = this.spool ? this.spool.status() : {};
    return {
      ...this.statusValue,
      pending: spool.pending || 0,
      filtered: spool.filtered || 0,
      failed: spool.failed || 0,
      lastDeliveredAt: spool.lastDeliveredAt || '',
      spoolError: spool.lastError || '',
      sources: Object.keys(this.state.sources || {}),
      updatedAt: iso(this.now()),
    };
  }
}

module.exports = { WeChatConnector, readPngSize, ENDPOINT };
