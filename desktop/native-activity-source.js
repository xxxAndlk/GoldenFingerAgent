'use strict';

const fs = require('node:fs');
const { readPageContent } = require('./page-content');
const { ContentMemory } = require('./content-dedup');
const {
  ActivitySource, clean, normalizeObservation, contentExcerpt, contextForObservation,
} = require('./observation');

// 默认采集源：保留现有前台窗口、UIA、正文区域裁剪、OCR 和语义去重链路。
class NativeActivitySource extends ActivitySource {
  constructor({ readActiveWindow, readPageContent: readContent = readPageContent, now = () => Date.now(), projectName = '' }) {
    super();
    this.readActiveWindow = readActiveWindow;
    this.readPageContent = readContent;
    this.now = now;
    this.projectName = projectName;
    this.reset();
  }

  reset() {
    this.lastWindow = '';
    this.lastContentAt = 0;
    this.lastWindowEventAt = 0;
    this.contentMemory = new ContentMemory();
  }

  async observe({ force = false, outputDir } = {}) {
    const window = await this.readActiveWindow();
    if (!window) return null;
    const app = clean(window.owner && window.owner.name, 120) || '未知应用';
    const title = clean(window.title);
    const key = `${app}\0${title}`;
    const keyChanged = key !== this.lastWindow;
    this.lastWindow = key;
    const ownApp = ['goldenfinger', 'electron'].includes(app.toLowerCase());
    if (ownApp || title === '金手指管家' || title === '工作轨迹' || title === 'Agent 与设置') return null;
    // 焦点在无标题/未知进程的占位窗口时直接跳过，避免把本应用的透明/悬浮窗口记进去。
    if (!title && app === '未知应用') return null;
    if (!keyChanged && !force && this.now() - this.lastContentAt < 6000) return null;
    this.lastContentAt = this.now();
    const page = await this.readPageContent({ windowKey: key, title, outputDir, at: this.now() });
    if (page.protectedContent || page.unchanged) return null;
    // 不记录本应用自己的页面（例如工作轨迹窗口自身的正文）。
    if (/\/desktop\/(index|activity|agents|settings|ccswitch)\.html/i.test(page.url || '')) return null;
    const rawContent = clean(page.text, 8000);
    const content = this.contentMemory.select(rawContent);
    const pageTitle = clean(page.activeTab || title);
    const context = contextForObservation(
      { title: pageTitle, url: page.url, content: rawContent },
      { projectName: this.projectName, at: this.now() },
    );
    const base = {
      app,
      title: pageTitle,
      windowTitle: title,
      activeTab: page.activeTab || pageTitle,
      url: clean(page.url, 1000),
      at: this.now(),
      evidenceRef: page.evidencePath || '',
      skipEvidence: Boolean(page.skipEvidence),
      captureNote: page.captureNote || '',
      contextKey: context.contextKey,
      projectCandidates: context.projectCandidates,
    };
    if (content) {
      return normalizeObservation({
        ...base,
        type: 'content',
        detail: contentExcerpt(content),
        content,
        source: page.source || 'uia',
      });
    }
    if (!rawContent) {
      const now = this.now();
      // 正文读不到（UI Automation / 截图不可用）时，至少定期记录当前窗口，
      // 避免工作轨迹长期只剩一条空记录。
      const due = keyChanged || now - this.lastWindowEventAt >= 60_000;
      if (!due) return null;
      this.lastWindowEventAt = now;
      return normalizeObservation({
        ...base,
        contextKey: contextForObservation({ title: pageTitle || title, url: page.url }, { projectName: this.projectName, at: now }).contextKey,
        type: 'window',
        detail: pageTitle || title || app,
        source: 'active-window',
      });
    }
    if (page.evidencePath) fs.rmSync(page.evidencePath, { force: true });
    return null;
  }
}

module.exports = { NativeActivitySource };
