'use strict';

const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

function profileName(site) {
  const account = String((site && site.account) || (site && site.id) || 'default');
  const hash = crypto.createHash('sha256').update(account).digest('hex').slice(0, 10);
  const id = String((site && site.id) || 'site').replace(/[^A-Za-z0-9_-]/g, '_').slice(0, 40);
  return `${id}-${hash}`;
}

function profileDir(rootDir, site) {
  return path.join(rootDir, profileName(site));
}

// BrowserSessionManager 只管理独立 profile 和人工登录接续；
// 不导出 Cookie/令牌，也不接触 SecretStore（自动登录另行实现）。
class BrowserSessionManager {
  constructor({ rootDir, chromium, logger = console, channel = 'msedge' }) {
    if (!chromium) throw new Error('Playwright chromium 未加载');
    this.rootDir = rootDir;
    this.chromium = chromium;
    this.logger = logger;
    this.channel = channel;
    this.contexts = new Map();
  }

  async contextFor(site, { headless = true } = {}) {
    const existing = this.contexts.get(site.id);
    if (existing) return existing;
    fs.mkdirSync(this.rootDir, { recursive: true });
    const userDataDir = profileDir(this.rootDir, site);
    fs.mkdirSync(userDataDir, { recursive: true });
    const options = {
      headless,
      viewport: { width: 1280, height: 900 },
      locale: 'zh-CN',
      acceptDownloads: false,
      bypassCSP: false,
    };
    let context;
    let usedChannel = this.channel;
    try {
      context = await this.chromium.launchPersistentContext(userDataDir, {
        ...options, channel: this.channel,
      });
    } catch (error) {
      if (this.channel === 'msedge') {
        usedChannel = 'chrome';
        try {
          context = await this.chromium.launchPersistentContext(userDataDir, {
            ...options, channel: 'chrome',
          });
        } catch (secondError) {
          throw new Error(`无法启动本机浏览器：${secondError.message || error.message}`);
        }
      } else {
        throw error;
      }
    }
    const entry = { context, userDataDir, headless, channel: usedChannel };
    this.contexts.set(site.id, entry);
    return entry;
  }

  async pageFor(site, url, options = {}) {
    const entry = await this.contextFor(site, options);
    const pages = entry.context.pages();
    const page = pages[0] || await entry.context.newPage();
    await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 45_000 });
    return { page, entry };
  }

  async openForLogin(site, url) {
    const entry = await this.contextFor(site, { headless: false });
    const pages = entry.context.pages();
    const page = pages[0] || await entry.context.newPage();
    if (url) {
      await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 45_000 }).catch(() => {});
    }
    await page.bringToFront().catch(() => {});
    return { page, entry };
  }

  async close(siteId) {
    const entry = this.contexts.get(siteId);
    if (!entry) return false;
    this.contexts.delete(siteId);
    await entry.context.close().catch(() => {});
    return true;
  }

  async closeAll() {
    const ids = [...this.contexts.keys()];
    for (const id of ids) await this.close(id);
  }

  get(siteId) {
    return this.contexts.get(siteId) || null;
  }
}

// 登录页探测只基于页面自身特征，不做自动绕过。
async function loginLikelyRequired(page) {
  try {
    return await page.evaluate(() => {
      const url = location.href.toLowerCase();
      if (/(login|signin|sign-in|passport|auth)/.test(url)) return true;
      return Boolean(document.querySelector('input[type="password"]'));
    });
  } catch {
    return false;
  }
}

module.exports = { BrowserSessionManager, profileName, profileDir, loginLikelyRequired };
