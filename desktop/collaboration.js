'use strict';

const { ContentMemory } = require('./content-dedup');

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
  const memory = new ContentMemory();
  const actions = [];
  for (const item of observations.filter(item => item.detail || item.content).slice(-60)) {
    const content = memory.select(item.content || item.detail, 600);
    if (!content) continue;
    const time = new Date(item.at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    actions.push(`${time} ${clean(item.app, 80)}《${clean(item.title, 160)}》：${content}`);
  }
  return `本阶段活动软件与窗口：${parts.join('；') || '没有采集到活动窗口'}。${actions.length ? `\n页面可见内容与操作轨迹：\n${actions.join('\n')}` : ''}`;
}

class CollaborationMode {
  constructor({
    readActiveWindow,
    api,
    onSummary = () => {},
    onSummarized = () => {},
    // 总结写入当前对话会话，避免每轮总结都新建一个会话。
    sessionId = () => '',
    onSession = () => {},
    now = () => Date.now(),
    scanIntervalMs = DEFAULT_SCAN_INTERVAL,
    summaryIntervalMs = DEFAULT_SUMMARY_INTERVAL,
  }) {
    this.readActiveWindow = readActiveWindow;
    this.api = api;
    this.onSummary = onSummary;
    this.onSummarized = onSummarized;
    this.sessionIdFor = sessionId;
    this.onSession = onSession;
    this.now = now;
    this.scanIntervalMs = scanIntervalMs;
    this.summaryIntervalMs = summaryIntervalMs;
    this.enabled = false;
    this.observations = [];
    this.scanTimer = null;
    this.scanning = false;
    this.summaryDue = 0;
    this.lastSummaryAt = 0;
  }

  status() {
    return {
      enabled: this.enabled,
      pendingScans: this.observations.length,
      scanIntervalMs: this.scanIntervalMs,
      summaryIntervalMs: this.summaryIntervalMs,
      nextSummaryAt: this.enabled ? new Date(this.summaryDue).toISOString() : null,
      lastSummaryAt: this.lastSummaryAt ? new Date(this.lastSummaryAt).toISOString() : null,
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

  addObservation(observation) {
    if (!this.enabled) return;
    this.observations.push({
      id: observation.id,
      app: clean(observation.app, 120),
      title: clean(observation.title, 240),
      detail: clean(observation.detail, 300),
      content: clean(observation.content, 1800),
      at: observation.at || this.now(),
    });
    this.observations = this.observations.slice(-2000);
  }

  async scanNow() {
    if (!this.enabled || this.scanning) return this.status();
    this.scanning = true;
    try {
      const window = await this.readActiveWindow();
      if (!this.enabled) return this.status();
      if (window) {
        const app = clean(window.owner && window.owner.name, 120);
        const title = clean(window.title, 240);
        const ownApp = ['goldenfinger', 'electron'].includes(app.toLowerCase());
        if (!ownApp && title !== '金手指管家' && title !== '工作轨迹' && title !== 'Agent 与设置') {
          this.observations.push({ app, title, at: this.now() });
        }
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
    const previous = this.lastSummary ? `\n上一阶段总结：${this.lastSummary}\n上一阶段已经说过且状态未变的内容不要重复。` : '';
    try {
      await this.api.capture(summary, '', '', 'desktop_collaboration', new Date(this.now()));
      const sessionId = this.sessionIdFor();
      const response = await this.api.chat(
        `以下是用户授权协作模式采集到的页面标题、去重后的页面正文和有效操作。原始截图仅保存在本机且未发送。请按“本阶段新增进展、仍在继续、待用户决定”组织三句话，只讲新增内容或状态变化，不要把未采集到的内容猜成事实；若没有实质变化就明确说没有，最后问一个帮助用户继续工作的问题。${previous}\n${summary}`,
        sessionId,
        AbortSignal.timeout(30_000),
        true,
      );
      if (!sessionId && response.session_id) this.onSession(response.session_id);
      this.lastSummary = response.reply;
      this.lastSummaryAt = this.now();
      this.onSummarized(observations);
      const result = { record: summary, reply: response.reply, at: new Date(this.lastSummaryAt).toISOString() };
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
