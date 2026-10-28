'use strict';

const DEFAULT_SCAN_INTERVAL = 30_000;
const DEFAULT_SUMMARY_INTERVAL = 15 * 60_000;

function clean(value, maxLength) {
  return String(value || '').replace(/\s+/g, ' ').trim().slice(0, maxLength);
}

function summarizeObservations(observations, scanIntervalMs = DEFAULT_SCAN_INTERVAL) {
  const apps = new Map();
  for (const observation of observations) {
    const app = clean(observation.app, 120) || '未知应用';
    if (!apps.has(app)) apps.set(app, { app, scans: 0, titles: [] });
    const entry = apps.get(app);
    entry.scans += 1;
    const title = clean(observation.title, 240);
    if (title && entry.titles[entry.titles.length - 1] !== title) entry.titles.push(title);
  }
  const ranked = [...apps.values()].sort((a, b) => b.scans - a.scans);
  const parts = ranked.map(entry => {
    const minutes = Math.max(1, Math.round(entry.scans * scanIntervalMs / 60_000));
    const titles = entry.titles.slice(-3).join('；');
    return `${entry.app} 约 ${minutes} 分钟${titles ? `（${titles}）` : ''}`;
  });
  return `本阶段活动软件与窗口：${parts.join('；') || '没有采集到活动窗口'}。`;
}

class CollaborationMode {
  constructor({
    readActiveWindow,
    api,
    onSummary = () => {},
    now = () => Date.now(),
    scanIntervalMs = DEFAULT_SCAN_INTERVAL,
    summaryIntervalMs = DEFAULT_SUMMARY_INTERVAL,
  }) {
    this.readActiveWindow = readActiveWindow;
    this.api = api;
    this.onSummary = onSummary;
    this.now = now;
    this.scanIntervalMs = scanIntervalMs;
    this.summaryIntervalMs = summaryIntervalMs;
    this.enabled = false;
    this.observations = [];
    this.scanTimer = null;
    this.scanning = false;
    this.summaryDue = 0;
  }

  status() {
    return {
      enabled: this.enabled,
      pendingScans: this.observations.length,
      scanIntervalMs: this.scanIntervalMs,
      summaryIntervalMs: this.summaryIntervalMs,
      nextSummaryAt: this.enabled ? new Date(this.summaryDue).toISOString() : null,
    };
  }

  async setEnabled(enabled) {
    this.enabled = Boolean(enabled);
    clearInterval(this.scanTimer);
    this.scanTimer = null;
    this.observations = [];
    if (this.enabled) {
      this.summaryDue = this.now() + this.summaryIntervalMs;
      await this.scanNow();
      this.scanTimer = setInterval(() => this.scanNow(), this.scanIntervalMs);
    }
    return this.status();
  }

  async scanNow() {
    if (!this.enabled || this.scanning) return this.status();
    this.scanning = true;
    try {
      const window = await this.readActiveWindow();
      if (!this.enabled) return this.status();
      if (window) {
        this.observations.push({
          app: clean(window.owner && window.owner.name, 120),
          title: clean(window.title, 240),
          at: this.now(),
        });
      }
      if (this.now() >= this.summaryDue) await this.summarizeNow();
    } catch (error) {
      this.onSummary({ error: `协作扫描暂时失败：${error.message}` });
    } finally {
      this.scanning = false;
    }
    return this.status();
  }

  async summarizeNow() {
    if (!this.enabled || this.observations.length === 0) {
      return { reply: '这段时间还没有采集到活动窗口。' };
    }
    const summary = summarizeObservations(this.observations, this.scanIntervalMs);
    const observations = this.observations;
    this.observations = [];
    this.summaryDue = this.now() + this.summaryIntervalMs;
    try {
      await this.api.capture(summary, '', '', 'desktop_collaboration', new Date(this.now()));
      const response = await this.api.chat(
        `以下是用户授权协作模式采集到的活动软件和窗口标题。请据此用三句话总结本阶段在做什么，不要推测标题之外的操作细节；最后问一个帮助用户继续工作的问题。\n${summary}`,
        '',
      );
      const result = { record: summary, reply: response.reply, at: new Date(this.now()).toISOString() };
      this.onSummary(result);
      return result;
    } catch (error) {
      // 总结失败时恢复尚未持久化的采样，等待下个周期，不静默丢数据。
      this.observations = [...observations, ...this.observations].slice(-2000);
      this.summaryDue = this.now() + 60_000;
      const result = { error: `协作总结暂时未送达：${error.message}` };
      this.onSummary(result);
      return result;
    }
  }
}

module.exports = {
  CollaborationMode,
  summarizeObservations,
  DEFAULT_SCAN_INTERVAL,
  DEFAULT_SUMMARY_INTERVAL,
};
