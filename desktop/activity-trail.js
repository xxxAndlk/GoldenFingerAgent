'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { InputMonitor } = require('./input-monitor');
const { readPageContent } = require('./page-content');
const { readUiContext } = require('./ui-context');
const { ContentMemory } = require('./content-dedup');
const { NativeActivitySource } = require('./native-activity-source');
const { clean, contentKey, normalizeObservation, selectNovelContent, contentExcerpt } = require('./observation');

function withTimeout(promise, ms, label) {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(label || '操作超时')), ms);
    }),
  ]).finally(() => clearTimeout(timer));
}

const EVIDENCE_TTL_MS = 3 * 24 * 60 * 60 * 1000;
const MAX_PERSISTED_EVENTS = 5000;
const LIST_LIMIT = 300;

function publicEvent(event) {
  const { evidencePath, evidenceRef, ...safe } = event;
  return {
    ...safe,
    hasEvidence: Boolean(evidencePath || evidenceRef),
    summarized: Boolean(event.summarizedAt),
  };
}

function readJSONL(file) {
  try {
    return fs.readFileSync(file, 'utf8').split(/\r?\n/).filter(Boolean).map(line => {
      try { return JSON.parse(line); } catch { return null; }
    }).filter(Boolean);
  } catch {
    return [];
  }
}

function appendJSONL(file, value) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.appendFileSync(file, JSON.stringify(value) + '\n');
}

function isMeaningfulAction(context, label) {
  if (!label || context.isPassword || !['Button', 'Hyperlink', 'MenuItem'].includes(context.control)) return false;
  return /发送|提交|保存|允许|确认|发布|运行|部署|创建|删除|下载|上传|安装|登录|开始|完成|send|save|allow|confirm|publish|run|deploy|create|delete|download|upload|install|sign in|start|done/i.test(label);
}

class ActivityTrail {
  constructor({ readActiveWindow, readPageContent: readContent = readPageContent, readUiContext: readContext = readUiContext, captureWindow, evidenceDir, dataDir, eventsPath, summariesPath, onEvent = () => {}, onError = () => {}, now = () => Date.now(), inputMonitor, inputMonitorEnabled = true, activitySource }) {
    this.readActiveWindow = readActiveWindow;
    this.readUiContext = readContext;
    this.captureWindow = captureWindow;
    this.evidenceDir = evidenceDir;
    this.dataDir = dataDir || evidenceDir;
    this.eventsPath = eventsPath || path.join(this.dataDir, 'activity-events.jsonl');
    this.summariesPath = summariesPath || path.join(this.dataDir, 'activity-summaries.jsonl');
    this.onEvent = onEvent;
    this.onError = onError;
    this.now = now;
    this.activitySource = activitySource || new NativeActivitySource({ readActiveWindow, readPageContent: readContent, now });
    this.inputMonitor = inputMonitor || new InputMonitor({ onEvent: event => this.handleInput(event), onError });
    // Low 完整性进程无法编译输入监听所需的 C#，跳过监听，避免反复报错；
    // 窗口与正文仍通过 UI Automation 轮询记录。
    this.inputMonitorEnabled = inputMonitorEnabled;
    this.enabled = false;
    this.events = this.loadEvents();
    this.timer = null;
    this.contentMemory = new ContentMemory();
    this.lastSubmission = null;
    this.counter = 0;
    this.busy = false;
  }

  // JSONL 追加式存储：事件一行；更新（总结标记/证据清理）也追加一行，启动时按 id 合并。
  // 这样不需要每次事件都重写整个文件，旧事件也不会被 300 条 UI 上限清掉。
  loadEvents() {
    const byID = new Map();
    const updates = [];
    for (const record of readJSONL(this.eventsPath)) {
      if (!record.id) continue;
      if (record.updated) {
        updates.push(record);
        continue;
      }
      byID.set(record.id, record);
    }
    for (const update of updates) {
      const event = byID.get(update.id);
      if (!event) continue;
      const { updated, ...fields } = update;
      Object.assign(event, fields);
    }
    return [...byID.values()]
      .sort((a, b) => Number(a.at || 0) - Number(b.at || 0))
      .slice(-MAX_PERSISTED_EVENTS);
  }

  persistEvent(event) {
    try { appendJSONL(this.eventsPath, event); } catch (error) { this.lastError = `轨迹持久化失败：${error.message}`; }
  }

  persistUpdate(fields) {
    try { appendJSONL(this.eventsPath, { updated: true, ...fields }); } catch (error) { this.lastError = `轨迹更新失败：${error.message}`; }
  }

  saveSummaryPhase(phase) {
    try { appendJSONL(this.summariesPath, phase); } catch (error) { this.lastError = `阶段总结持久化失败：${error.message}`; }
  }

  lastSummary() {
    const phases = readJSONL(this.summariesPath);
    for (let i = phases.length - 1; i >= 0; i -= 1) {
      if (phases[i] && phases[i].summary) return phases[i].summary;
    }
    return '';
  }

  // 供主对话的“已产生 N 条总结”入口使用；按会话过滤，兼容旧记录没有 sessionId 的情况。
  listSummaryPhases(sessionId = '', limit = 50) {
    const id = String(sessionId || '').trim();
    const phases = readJSONL(this.summariesPath).filter(phase => phase && phase.summary);
    const filtered = id
      ? phases.filter(phase => !phase.sessionId || phase.sessionId === id)
      : phases.filter(phase => !phase.sessionId);
    return filtered.slice(-limit).map(phase => ({ ...phase }));
  }

  // 主对话每轮携带当天轨迹摘要与最近活动，让模型不必用工具反复翻找。
  // 只返回本机数据，由 Go 端作为“只读数据”附加到本轮请求，不写入会话原文。
  chatContext({ now = this.now(), maxChars = 12000 } = {}) {
    const start = new Date(now);
    start.setHours(0, 0, 0, 0);
    const end = new Date(start);
    end.setDate(end.getDate() + 1);
    const inToday = value => {
      const at = Number(value || 0);
      return at >= start.getTime() && at < end.getTime();
    };
    const clock = value => {
      const date = new Date(Number(value));
      return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
    };
    const day = `${start.getFullYear()}-${String(start.getMonth() + 1).padStart(2, '0')}-${String(start.getDate()).padStart(2, '0')}`;
    const phases = readJSONL(this.summariesPath).filter(phase => phase && phase.summary && inToday(phase.completedAt || phase.endedAt));
    const events = this.events.filter(event => event && inToday(event.at));
    if (!phases.length && !events.length) return '';

    const lines = [`日期：${day}；阶段总结 ${phases.length} 条，活动记录 ${events.length} 条。`];
    if (phases.length) {
      lines.push('【阶段总结】');
      for (const phase of phases.slice(-8)) {
        lines.push(`- ${clock(phase.completedAt || phase.endedAt)} ${clean(phase.summary, 1600)}`);
      }
    }
    if (events.length) {
      lines.push('【最近活动】');
      let previous = '';
      for (const event of events.slice(-80)) {
        const app = clean(event.app, 60) || '未知应用';
        const title = clean(event.title, 100);
        const detail = clean(event.detail, 240);
        const line = `- ${clock(event.at)} [${app}] ${title}${detail && detail !== title ? `：${detail}` : ''}`;
        if (line === previous) continue;
        previous = line;
        lines.push(line);
      }
    }
    let text = lines.join('\n');
    if (text.length > maxChars) text = Array.from(text).slice(0, maxChars - 1).join('') + '…';
    return text;
  }

  pendingObservations(limit = 2000) {
    return this.events
      .filter(event => !event.summarizedAt)
      .slice(-limit)
      .map(event => ({
        id: event.id,
        app: event.app,
        title: event.title,
        detail: event.detail,
        content: event.content,
        at: event.at,
      }));
  }

  status() {
    return {
      activityEnabled: this.enabled,
      eventCount: this.events.length,
      evidenceCount: this.events.filter(event => event.evidencePath).length,
      activityBusy: this.busy,
      activityTimer: Boolean(this.timer),
      lastObservedAt: this.lastObservedAt || 0,
      lastEventAt: this.lastEventAt || 0,
      lastActivityError: this.lastError || '',
    };
  }

  async start() {
    if (this.enabled) return this.status();
    this.enabled = true;
    this.activitySource.reset();
    this.contentMemory = new ContentMemory();
    this.lastSubmission = null;
    fs.mkdirSync(this.evidenceDir, { recursive: true });
    this.removeExpiredEvidence();
    await this.observeWindow();
    this.timer = setInterval(() => this.observeWindow(), 2000);
    this.cleanupTimer = setInterval(() => this.removeExpiredEvidence(), 60 * 60 * 1000);
    if (this.inputMonitorEnabled) this.inputMonitor.start();
    return this.status();
  }

  stop() {
    this.enabled = false;
    clearInterval(this.timer);
    clearInterval(this.cleanupTimer);
    clearTimeout(this.contentRefreshTimer);
    this.timer = null;
    this.cleanupTimer = null;
    this.activitySource.stop();
    this.inputMonitor.stop();
    return this.status();
  }

  async observeWindow(force = false) {
    if (!this.enabled || this.busy) return;
    this.busy = true;
    try {
      this.lastObservedAt = this.now();
      // 单个窗口的截图/OCR 可能卡住（例如受保护窗口）；超时后必须释放 busy，
      // 否则后续 2 秒轮询都会直接返回，轨迹永久空白。
      const observation = await withTimeout(
        this.activitySource.observe({ force, outputDir: this.evidenceDir }),
        35_000,
        '页面观察超时',
      );
      if (!this.enabled || !observation || observation.privacyLevel === 'protected') return;
      await this.record(observation, true, observation.evidenceRef);
    } catch (error) {
      this.lastError = error.message;
      this.onError(`页面内容观察失败：${error.message}`);
    } finally {
      this.busy = false;
    }
  }

  handleInput(input) {
    if (!this.enabled) return Promise.resolve();
    this.inputQueue = (this.inputQueue || Promise.resolve()).then(() => this.processInput(input)).catch(error => {
      this.onError(`操作观察失败：${error.message}`);
    });
    return this.inputQueue;
  }

  scheduleContentRefresh(delay = 650) {
    clearTimeout(this.contentRefreshTimer);
    this.contentRefreshTimer = setTimeout(() => this.observeWindow(true), delay);
  }

  async processInput(input) {
    if (!this.enabled) return;
    const window = await this.readActiveWindow();
    if (!window || !this.enabled) return;
    const app = clean(window.owner && window.owner.name, 120) || '未知应用';
    const title = clean(window.title);
    if (app.toLowerCase() === 'goldenfinger' || title === '金手指管家' || title === '工作轨迹') return;
    if (input.type === 'click') {
      let context = {};
      try { context = await this.readUiContext(input.x, input.y); } catch { /* 页面变化仍会由截图观察。 */ }
      const label = clean(context.name, 160);
      if (isMeaningfulAction(context, label)) {
        await this.record({ type: 'action', app, title, detail: `点击“${label}”`, source: 'uia', at: this.now() }, false);
      }
      this.scheduleContentRefresh();
      return;
    }
    if (input.key === 'Enter') {
      const submitted = clean(input.text, 1800);
      const signature = contentKey(submitted);
      if (submitted && (!this.lastSubmission || this.lastSubmission.signature !== signature || this.now() - this.lastSubmission.at > 2000)) {
        this.lastSubmission = { signature, at: this.now() };
        this.contentMemory.add(submitted);
        await this.record({ type: 'submission', app, title, detail: `提交：${contentExcerpt(submitted) || clean(submitted, 180)}`, content: submitted, source: 'focused-edit', at: this.now() }, false);
      }
      this.scheduleContentRefresh();
      return;
    }
    await this.record({ type: input.type, app, title, detail: input.key || '快捷键', at: this.now() }, false);
  }

  async record(event, withEvidence, existingEvidence = '') {
    const entry = { ...normalizeObservation(event), id: `${Number(event.at).toString(36)}-${this.now().toString(36)}-${++this.counter}` };
    if (existingEvidence) entry.evidencePath = existingEvidence;
    else if (withEvidence && !entry.skipEvidence) {
      try {
        entry.evidencePath = await withTimeout(
          this.captureWindow({ title: entry.title, outputDir: this.evidenceDir, at: entry.at }),
          8_000,
          '截图超时',
        );
      } catch (error) { entry.captureNote = error.message; }
    }
    this.events.push(entry);
    this.events = this.events.slice(-MAX_PERSISTED_EVENTS);
    this.persistEvent(entry);
    this.lastEventAt = this.now();
    this.onEvent(publicEvent(entry));
    return entry;
  }

  list() { return this.events.slice(-LIST_LIMIT).map(publicEvent); }

  evidence(id) {
    const index = this.events.findIndex(event => event.id === id);
    if (index < 0) throw new Error('这条操作已被清理');
    const asDataUrl = file => `data:image/${path.extname(file).toLowerCase() === '.png' ? 'png' : 'jpeg'};base64,${fs.readFileSync(file).toString('base64')}`;
    const event = this.events[index];
    if (event.evidencePath && fs.existsSync(event.evidencePath)) {
      return { event: publicEvent(event), dataUrl: asDataUrl(event.evidencePath), approximate: false };
    }
    // 本地补证：仅找同窗口、前后 8 秒的邻近截图，不操作用户当前页面。
    const neighbor = this.events.slice(Math.max(0, index - 5), index + 6)
      .filter(candidate => candidate.evidencePath && candidate.app === event.app && candidate.title === event.title && Math.abs(candidate.at - event.at) <= 8000)
      .sort((a, b) => Math.abs(a.at - event.at) - Math.abs(b.at - event.at))[0];
    if (!neighbor || !fs.existsSync(neighbor.evidencePath)) throw new Error('没有本地证据；为保护当前页面，未自动跳转补看');
    return { event: publicEvent(event), dataUrl: asDataUrl(neighbor.evidencePath), approximate: true };
  }

  // 总结成功后只做标记，不删截图；截图从总结时刻起保留 3 天，由 removeExpiredEvidence 清理。
  markSummarized(ids, phaseId = '', summarizedAt = this.now()) {
    const summarized = new Set((ids || []).filter(Boolean));
    let marked = 0;
    for (const event of this.events) {
      if (!summarized.has(event.id)) continue;
      event.summarizedAt = summarizedAt;
      event.phaseId = phaseId;
      event.evidenceExpiresAt = summarizedAt + EVIDENCE_TTL_MS;
      this.persistUpdate({
        id: event.id,
        summarizedAt: event.summarizedAt,
        phaseId: event.phaseId,
        evidenceExpiresAt: event.evidenceExpiresAt,
      });
      marked += 1;
    }
    return marked;
  }

  clear() {
    try {
      for (const name of fs.readdirSync(this.evidenceDir)) {
        if (name.endsWith('.jsonl')) continue;
        const file = path.join(this.evidenceDir, name);
        try { if (fs.statSync(file).isFile()) fs.rmSync(file, { force: true }); } catch { /* 下轮过期清理 */ }
      }
    } catch { /* 目录尚不存在 */ }
    this.events = [];
    this.counter = 0;
    try { fs.mkdirSync(this.dataDir, { recursive: true }); fs.writeFileSync(this.eventsPath, ''); fs.writeFileSync(this.summariesPath, ''); } catch (error) { this.lastError = `清空轨迹存储失败：${error.message}`; }
    this.activitySource.reset();
    this.contentMemory = new ContentMemory();
    this.lastSubmission = null;
  }

  removeExpiredEvidence() {
    const now = this.now();
    const keep = new Set();
    for (const event of this.events) {
      if (!event.evidencePath) continue;
      const base = Number(event.summarizedAt || event.at || 0);
      const expiresAt = Number(event.evidenceExpiresAt || base + EVIDENCE_TTL_MS);
      if (now >= expiresAt) {
        try { fs.rmSync(event.evidencePath, { force: true }); } catch { /* 下次再试 */ }
        event.evidencePath = '';
        this.persistUpdate({ id: event.id, evidencePath: '', evidenceDeletedAt: now });
      } else {
        keep.add(path.resolve(event.evidencePath));
      }
    }
    // 没有对应事件引用的孤儿截图也按 3 天清理；持久化 JSONL 不在此列。
    try {
      for (const name of fs.readdirSync(this.evidenceDir)) {
        if (name.endsWith('.jsonl')) continue;
        const file = path.join(this.evidenceDir, name);
        try {
          const info = fs.statSync(file);
          if (info.isFile() && info.mtimeMs < now - EVIDENCE_TTL_MS && !keep.has(path.resolve(file))) fs.rmSync(file, { force: true });
        } catch { /* 保留无法处理的文件 */ }
      }
    } catch { /* 目录尚不存在 */ }
  }
}

module.exports = {
  ActivityTrail,
  publicEvent,
  selectNovelContent,
  contentExcerpt,
  withTimeout,
  EVIDENCE_TTL_MS,
  LIST_LIMIT,
};
