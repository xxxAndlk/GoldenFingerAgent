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

function publicEvent(event) {
  const { evidencePath, evidenceRef, ...safe } = event;
  return { ...safe, hasEvidence: Boolean(evidencePath || evidenceRef) };
}

function isMeaningfulAction(context, label) {
  if (!label || context.isPassword || !['Button', 'Hyperlink', 'MenuItem'].includes(context.control)) return false;
  return /发送|提交|保存|允许|确认|发布|运行|部署|创建|删除|下载|上传|安装|登录|开始|完成|send|save|allow|confirm|publish|run|deploy|create|delete|download|upload|install|sign in|start|done/i.test(label);
}

class ActivityTrail {
  constructor({ readActiveWindow, readPageContent: readContent = readPageContent, readUiContext: readContext = readUiContext, captureWindow, evidenceDir, onEvent = () => {}, onError = () => {}, now = () => Date.now(), inputMonitor, inputMonitorEnabled = true, activitySource }) {
    this.readActiveWindow = readActiveWindow;
    this.readUiContext = readContext;
    this.captureWindow = captureWindow;
    this.evidenceDir = evidenceDir;
    this.onEvent = onEvent;
    this.onError = onError;
    this.now = now;
    this.activitySource = activitySource || new NativeActivitySource({ readActiveWindow, readPageContent: readContent, now });
    this.inputMonitor = inputMonitor || new InputMonitor({ onEvent: event => this.handleInput(event), onError });
    // Low 完整性进程无法编译输入监听所需的 C#，跳过监听，避免反复报错；
    // 窗口与正文仍通过 UI Automation 轮询记录。
    this.inputMonitorEnabled = inputMonitorEnabled;
    this.enabled = false;
    this.events = [];
    this.timer = null;
    this.contentMemory = new ContentMemory();
    this.lastSubmission = null;
    this.counter = 0;
    this.busy = false;
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
        20_000,
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
    const entry = { ...normalizeObservation(event), id: `${Number(event.at).toString(36)}-${++this.counter}` };
    if (existingEvidence) entry.evidencePath = existingEvidence;
    else if (withEvidence) {
      try {
        entry.evidencePath = await withTimeout(
          this.captureWindow({ title: entry.title, outputDir: this.evidenceDir, at: entry.at }),
          8_000,
          '截图超时',
        );
      } catch (error) { entry.captureNote = error.message; }
    }
    this.events.push(entry);
    this.events = this.events.slice(-300);
    this.lastEventAt = this.now();
    this.onEvent(publicEvent(entry));
    return entry;
  }

  list() { return this.events.map(publicEvent); }

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

  deleteSummarizedEvidence(ids) {
    const summarized = new Set(ids);
    for (const event of this.events) {
      if (!summarized.has(event.id) || !event.evidencePath) continue;
      try { fs.rmSync(event.evidencePath, { force: true }); } catch { /* 删除失败时由过期清理重试 */ }
      event.evidencePath = '';
    }
  }

  clear() {
    for (const name of fs.readdirSync(this.evidenceDir)) {
      const file = path.join(this.evidenceDir, name);
      try { if (fs.statSync(file).isFile()) fs.rmSync(file, { force: true }); } catch { /* 下轮过期清理 */ }
    }
    this.events = [];
    this.activitySource.reset();
    this.contentMemory = new ContentMemory();
    this.lastSubmission = null;
  }

  removeExpiredEvidence() {
    const cutoff = this.now() - 24 * 60 * 60 * 1000;
    for (const name of fs.readdirSync(this.evidenceDir)) {
      const file = path.join(this.evidenceDir, name);
      try { if (fs.statSync(file).isFile() && fs.statSync(file).mtimeMs < cutoff) fs.rmSync(file); } catch { /* 保留无法处理的文件 */ }
    }
  }
}

module.exports = { ActivityTrail, publicEvent, selectNovelContent, contentExcerpt, withTimeout };
