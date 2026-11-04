'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { eventId } = require('./observation-envelope');

const MAX_RECORDS = 500;
const RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_BACKOFF_MS = 5 * 60_000;

function readRecords(file) {
  try {
    const value = JSON.parse(fs.readFileSync(file, 'utf8'));
    return Array.isArray(value) ? value.filter(record => record && record.id) : [];
  } catch {
    return [];
  }
}

function atomicWrite(file, records) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const temp = `${file}.tmp`;
  fs.writeFileSync(temp, JSON.stringify(records), 'utf8');
  fs.renameSync(temp, file);
}

// 至少一次投递的本地队列。只缓存观察事件；外部动作仍由 ActionOutbox 人工审批。
class ObservationSpool {
  constructor({ filePath, deliver, now = () => Date.now(), logger = console } = {}) {
    this.filePath = filePath;
    this.deliver = deliver;
    this.now = now;
    this.logger = logger;
    this.records = filePath ? readRecords(filePath) : [];
    this.flushing = null;
    this.lastDeliveredAt = this.records.reduce((latest, record) =>
      Math.max(latest, Number(record.delivered_at || 0)), 0);
    this.lastError = '';
    this.deliveredTotal = this.records.filter(record => record.status === 'delivered').length;
    this.trim();
  }

  persist() {
    if (!this.filePath) return true;
    try {
      atomicWrite(this.filePath, this.records);
      return true;
    } catch (error) {
      this.lastError = `待发队列保存失败：${error.message}`;
      return false;
    }
  }

  trim() {
    const now = this.now();
    const terminal = new Set(['delivered', 'filtered', 'failed']);
    this.records = this.records
      .filter(record => !terminal.has(record.status) || now - Number(record.updated_at || record.created_at || 0) < RETENTION_MS)
      .slice(-MAX_RECORDS);
  }

  enqueue({ endpoint, payload, kind = '', sourceKey = '', event = '' }) {
    const now = this.now();
    const record = {
      id: event || payload && (payload.event_id || payload.message_id) || eventId(),
      endpoint: String(endpoint || ''),
      kind: String(kind || ''),
      sourceKey: String(sourceKey || ''),
      payload,
      status: 'pending',
      attempts: 0,
      next_attempt_at: now,
      created_at: now,
      updated_at: now,
      last_error: '',
    };
    this.records.push(record);
    this.trim();
    record.persisted = this.persist();
    return record;
  }

  pending(limit = 20) {
    const now = this.now();
    return this.records
      .filter(record => record.status === 'pending' && Number(record.next_attempt_at || 0) <= now)
      .slice(0, limit);
  }

  dropBody(record, reason) {
    record.payload = {
      event_id: record.id,
      dropped: true,
      reason: String(reason || '').slice(0, 300),
    };
    record.sourceKey = '';
  }

  // revokeSource 清除某个来源尚未投递的正文。授权撤销后这些消息
  // 不该继续留在本地队列里等待投递。
  revokeSource(sourceKey) {
    const target = String(sourceKey || '');
    if (!target) return 0;
    let revoked = 0;
    for (const record of this.records) {
      if (record.status !== 'pending' || record.sourceKey !== target) continue;
      record.status = 'filtered';
      record.last_error = '授权已撤销';
      record.filtered_at = this.now();
      record.updated_at = record.filtered_at;
      this.dropBody(record, '授权已撤销');
      revoked += 1;
    }
    if (revoked > 0) {
      this.trim();
      this.persist();
    }
    return revoked;
  }

  async flush(limit = 20) {
    if (this.flushing) return this.flushing;
    this.flushing = (async () => {
      for (const record of this.pending(limit)) {
        if (!this.deliver || !record.endpoint) {
          record.status = 'failed';
          record.last_error = '未配置投递器';
          record.updated_at = this.now();
          this.dropBody(record, record.last_error);
          this.persist();
          continue;
        }
        try {
          const response = await this.deliver(record);
          const status = Number(response && response.status);
          const body = response && response.body;
          if (status >= 200 && status < 300) {
            record.status = body && body.filtered ? 'filtered' : 'delivered';
            record.updated_at = this.now();
            if (record.status === 'filtered') {
              record.last_error = String((body && body.reason) || '服务端过滤');
              record.filtered_at = record.updated_at;
              this.dropBody(record, record.last_error);
            } else {
              record.delivered_at = record.updated_at;
              this.lastDeliveredAt = record.delivered_at;
              this.deliveredTotal += 1;
            }
            this.lastError = '';
          } else if (status >= 400 && status < 500 && status !== 429) {
            record.status = 'failed';
            record.last_error = `HTTP ${status}`;
            record.updated_at = this.now();
            this.dropBody(record, record.last_error);
            this.lastError = record.last_error;
          } else {
            throw Object.assign(new Error(`HTTP ${status || 'network'}`), { retryable: true });
          }
        } catch (error) {
          record.attempts = Number(record.attempts || 0) + 1;
          record.last_error = String(error.message || error).slice(0, 300);
          record.updated_at = this.now();
          const backoff = Math.min(MAX_BACKOFF_MS, 5000 * (2 ** Math.min(record.attempts, 6)));
          record.next_attempt_at = this.now() + backoff;
          record.status = 'pending';
          this.lastError = record.last_error;
        }
        this.persist();
      }
      this.trim();
      this.persist();
    })().finally(() => { this.flushing = null; });
    return this.flushing;
  }

  status() {
    const count = status => this.records.filter(record => record.status === status).length;
    const pending = count('pending');
    return {
      queueLength: this.records.length,
      pending,
      delivered: count('delivered'),
      filtered: count('filtered'),
      failed: count('failed'),
      deliveredTotal: this.deliveredTotal,
      lastDeliveredAt: this.lastDeliveredAt ? new Date(this.lastDeliveredAt).toISOString() : '',
      lastError: this.lastError,
    };
  }

  stop() {}
}

module.exports = { ObservationSpool, MAX_RECORDS, RETENTION_MS };
