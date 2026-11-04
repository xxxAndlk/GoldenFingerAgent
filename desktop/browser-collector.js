'use strict';

const fs = require('node:fs');
const path = require('node:path');
const {
  canonicalUrl, checkUrl, sanitizeSiteLimits, siteReady, urlOrigin,
} = require('./browser-policy');
const { extractPage, makeWebItem, contentHash } = require('./browser-extract');
const { loginLikelyRequired } = require('./browser-session');

function readState(file) {
  try {
    const value = JSON.parse(fs.readFileSync(file, 'utf8'));
    return value && typeof value === 'object' ? value : {};
  } catch {
    return {};
  }
}

// BrowserCollector 负责单站点 single-flight、有限导航和结果上报。
// 它不点击业务按钮、不填写表单、不绕过验证码；外部动作仍走 ActionOutbox。
class BrowserCollector {
  constructor({ sessions, captureItem, logger = console, now = () => Date.now(), statePath = '' }) {
    this.sessions = sessions;
    this.captureItem = captureItem;
    this.logger = logger;
    this.now = now;
    this.statePath = statePath;
    this.running = new Map();
    this.status = new Map();
    const saved = statePath ? readState(statePath) : {};
    for (const [siteId, value] of Object.entries(saved.sites || {})) {
      if (value && typeof value === 'object') {
        this.status.set(siteId, {
          running: false, lastRunAt: 0, lastSuccessAt: 0, lastError: '', items: 0,
          loginRequired: false, pages: {}, unchanged: 0, ...value, running: false,
        });
      }
    }
  }

  saveState() {
    if (!this.statePath) return;
    try {
      fs.mkdirSync(path.dirname(this.statePath), { recursive: true });
      const sites = {};
      for (const [siteId, value] of this.status.entries()) {
        if (value && typeof value === 'object') sites[siteId] = value;
      }
      const temp = `${this.statePath}.tmp`;
      fs.writeFileSync(temp, JSON.stringify({ sites }), 'utf8');
      fs.renameSync(temp, this.statePath);
    } catch (error) {
      this.logger.error?.(`网页采集检查点保存失败：${error.message}`);
    }
  }

  statusFor(siteId) {
    return this.status.get(siteId) || {
      running: false, lastRunAt: 0, lastSuccessAt: 0, lastError: '', items: 0,
      loginRequired: false, pages: {}, unchanged: 0,
    };
  }

  health() {
    return {
      sites: [...this.status.entries()].map(([siteId, value]) => ({ siteId, ...value })),
      running: this.running.size,
      pending: 0,
    };
  }

  async runSite(site, options = {}) {
    if (!siteReady(site)) throw new Error('站点未启用或缺少起始 URL');
    const current = this.running.get(site.id);
    if (current) return current;
    const task = this.collect(site, options).finally(() => this.running.delete(site.id));
    this.running.set(site.id, task);
    return task;
  }

  async collect(site, options) {
    const limits = sanitizeSiteLimits(site);
    const headless = options.headless !== undefined ? Boolean(options.headless) : Boolean(site.session_enabled);
    const visitLimit = Math.min(limits.maxPages, Number(options.maxPages) || limits.maxPages);
    const deadline = this.now() + (Number(options.timeoutMs) || 60_000);
    const previous = this.statusFor(site.id);
    const state = {
      running: true, lastRunAt: this.now(), lastSuccessAt: Number(previous.lastSuccessAt) || 0,
      lastError: '', items: 0, loginRequired: false, pages: { ...(previous.pages || {}) },
      unchanged: 0, nextDueAt: this.now() + limits.intervalSeconds * 1000,
    };
    this.status.set(site.id, state);
    this.saveState();
    const queue = [];
    const seen = new Set();
    for (const raw of site.start_urls || []) {
      try {
        const url = checkUrl(raw, site);
        if (!seen.has(url)) {
          seen.add(url);
          queue.push({ url, depth: 0 });
        }
      } catch (error) {
        state.lastError = error.message;
      }
    }
    let visited = 0;
    try {
      while (queue.length && visited < visitLimit) {
        if (this.now() > deadline) {
          state.lastError = '本轮采集超过时间上限，已停止';
          break;
        }
        const { url, depth } = queue.shift();
        let page;
        let entry;
        try {
          ({ page, entry } = await this.sessions.pageFor(site, url, { headless }));
        } catch (error) {
          state.lastError = error.message;
          break;
        }
        if (await loginLikelyRequired(page)) {
          state.loginRequired = true;
          state.lastError = '需要人工登录或二次验证；采集已停止';
          break;
        }
        const extraction = await extractPage(page, { canonicalUrl: url });
        extraction.itemId = canonicalUrl(extraction.canonicalUrl || url);
        const item = makeWebItem({
          site, account: site.account || site.id, url, extraction,
        });
        const pageKey = item.canonical_url || item.url;
        const hash = contentHash(item.text);
        const checkpoint = state.pages[pageKey];
        item.change = checkpoint ? 'updated' : 'new';
        if (checkpoint && checkpoint.hash === hash) {
          checkpoint.lastSeenAt = this.now();
          state.unchanged += 1;
          this.saveState();
        } else {
          const result = await this.captureItem(item);
          if (result && result.filtered) {
            state.lastError = `页面被服务端过滤：${result.reason || '策略限制'}`;
          } else {
            state.pages[pageKey] = {
              hash,
              change: checkpoint ? 'updated' : 'new',
              lastSeenAt: this.now(),
              lastChangedAt: this.now(),
              title: item.title,
            };
            state.items += 1;
            state.lastSuccessAt = this.now();
            this.saveState();
          }
        }
        visited += 1;
        if (!site.navigation_enabled || depth >= limits.maxDepth) continue;
        for (const link of extraction.links || []) {
          if (queue.length + visited >= visitLimit) break;
          let next;
          try {
            next = checkUrl(link.href, site);
          } catch {
            continue;
          }
          if (!seen.has(next)) {
            seen.add(next);
            queue.push({ url: next, depth: depth + 1 });
          }
        }
        if (!entry) break;
      }
      return { siteId: site.id, ...state, running: false, visited };
    } catch (error) {
      state.lastError = error.message;
      return { siteId: site.id, ...state, running: false, visited };
    } finally {
      state.running = false;
      state.nextDueAt = this.now() + limits.intervalSeconds * 1000;
      if (!this.sessions.get(site.id)) {
        this.status.set(site.id, state);
      }
      this.saveState();
    }
  }

  async readCurrent(site, rawUrl) {
    const url = checkUrl(rawUrl, site);
    const { page } = await this.sessions.pageFor(site, url, {
      headless: Boolean(site.session_enabled),
    });
    const extraction = await extractPage(page, { canonicalUrl: url });
    return {
      url,
      title: extraction.title,
      text: extraction.text,
      extractedBy: extraction.extractedBy,
      completeness: extraction.completeness,
      redacted: extraction.redacted,
      links: (extraction.links || []).slice(0, 50),
    };
  }

  // autoLogin 只在主进程确认 origin/登录地址/选择器后调用；凭据不落日志、不返回渲染进程。
  async autoLogin(site, credential) {
    if (!site.auto_login_enabled) throw new Error('站点未开启自动登录');
    if (!site.login_url || !site.username_selector || !site.password_selector || !site.submit_selector) {
      throw new Error('自动登录缺少确定性表单选择器');
    }
    const loginUrl = checkUrl(site.login_url, site);
    if (!loginUrl.toLowerCase().startsWith('https://')) throw new Error('自动登录只允许 HTTPS');
    const origin = urlOrigin(loginUrl);
    if (String(credential.origin || '').toLowerCase() !== origin) {
      throw new Error('凭据 origin 与登录地址不一致，已停止');
    }
    const { page } = await this.sessions.pageFor(site, loginUrl, {
      headless: Boolean(site.session_enabled),
    });
    if (origin !== urlOrigin(page.url())) {
      await this.sessions.close(site.id);
      throw new Error('登录页跳转到未授权 origin，已停止');
    }
    try {
      await page.fill(site.username_selector, credential.username, { timeout: 8000 });
      await page.fill(site.password_selector, credential.password, { timeout: 8000 });
      await Promise.all([
        page.waitForNavigation({ timeout: 10_000 }).catch(() => {}),
        page.click(site.submit_selector, { timeout: 8000 }),
      ]);
    } catch {
      return {
        status: 'awaiting_user', url: page.url(),
        message: '表单结构变化或页面未响应；已停止自动填写，请人工登录。',
      };
    }
    await page.waitForTimeout(1500);
    if (await loginLikelyRequired(page)) {
      return {
        status: 'awaiting_user', url: page.url(),
        message: '检测到验证码、二次验证或登录未完成；已停止，请人工接续。',
      };
    }
    try {
      checkUrl(page.url(), site);
    } catch {
      await this.sessions.close(site.id);
      throw new Error('登录后跳转到未授权地址，已关闭会话');
    }
    const finalUrl = page.url();
    await this.sessions.close(site.id);
    return { status: 'auto_logged_in', url: finalUrl };
  }

  async loginSite(site, url) {
    const start = url || (site.start_urls && site.start_urls[0]);
    if (!start) throw new Error('站点没有起始 URL');
    const safe = checkUrl(start, site);
    const { page } = await this.sessions.openForLogin(site, safe);
    return {
      status: 'awaiting_user', url: safe,
      message: '已打开独立浏览器窗口。请人工完成登录/扫码/二次验证；完成后关闭窗口或点击停止，再执行采集。',
      currentUrl: page.url(),
    };
  }

  async stop(siteId) {
    this.running.delete(siteId);
    return this.sessions.close(siteId);
  }

  async stopAll() {
    this.running.clear();
    await this.sessions.closeAll();
  }

  // schedulerTick 由主进程定时调用；同一站点仍受 single-flight 保护。
  async schedulerTick(sites) {
    const results = [];
    for (const site of sites || []) {
      if (!site.enabled) continue;
      const status = this.statusFor(site.id);
      const interval = sanitizeSiteLimits(site).intervalSeconds * 1000;
      if (status.running || this.now() - status.lastRunAt < interval) continue;
      results.push(await this.runSite(site));
    }
    return results;
  }
}

module.exports = { BrowserCollector };
