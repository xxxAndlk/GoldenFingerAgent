'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { BrowserCollector } = require('../browser-collector');

function fakePage({ login = false, links = [] } = {}) {
  return {
    async addScriptTag() {},
    async evaluate(fn) {
      const source = String(fn);
      if (source.includes('input[type="password"]')) return login;
      return {
        title: '测试页面',
        text: '页面正文内容足够长，用于测试采集链路。',
        url: 'https://example.com/items/1',
        links,
        extractedBy: 'dom',
        completeness: 'partial',
      };
    },
  };
}

function fakeSessions(options = {}) {
  return {
    calls: [],
    pageFor(site, url) {
      this.calls.push(url);
      return { page: fakePage(options), entry: {} };
    },
    get() { return {}; },
    async close() { return true; },
    async closeAll() {},
  };
}

const site = {
  id: 'example',
  account: 'me',
  enabled: true,
  session_enabled: true,
  summary_enabled: true,
  brief_enabled: true,
  start_urls: ['https://example.com/items/1'],
  allowed_origins: ['https://example.com'],
  allowed_path_prefixes: ['/items/'],
  max_pages: 2,
  max_depth: 1,
  interval_seconds: 60,
};

test('collector captures a page and deduplicates concurrent runs', async () => {
  const items = [];
  const sessions = fakeSessions();
  const collector = new BrowserCollector({
    sessions,
    captureItem: async item => items.push(item),
    now: () => 1000,
  });
  const [first, second] = await Promise.all([
    collector.runSite(site, { timeoutMs: 5000 }),
    collector.runSite(site, { timeoutMs: 5000 }),
  ]);
  assert.equal(items.length, 1);
  assert.equal(first.siteId, 'example');
  assert.equal(second.items, 1);
  assert.equal(sessions.calls.length, 1);
});

test('collector stops when login is required', async () => {
  const items = [];
  const collector = new BrowserCollector({
    sessions: fakeSessions({ login: true }),
    captureItem: async item => items.push(item),
    now: () => 1000,
  });
  const result = await collector.runSite(site, { timeoutMs: 5000 });
  assert.equal(result.loginRequired, true);
  assert.equal(items.length, 0);
});

test('autoLogin fills deterministic selectors and closes the session', async () => {
  const fills = [];
  let url = 'https://example.com/login';
  let closed = '';
  const page = {
    url: () => url,
    fill: async (selector, value) => { fills.push([selector, value]); },
    click: async () => { url = 'https://example.com/home'; },
    waitForNavigation: async () => {},
    waitForTimeout: async () => {},
    evaluate: async () => false,
  };
  const sessions = {
    calls: 0,
    pageFor() { this.calls += 1; return { page, entry: {} }; },
    get() { return {}; },
    async close(siteId) { closed = siteId; return true; },
    async closeAll() {},
  };
  const collector = new BrowserCollector({ sessions, captureItem: async () => {}, now: () => 1000 });
  const result = await collector.autoLogin({
    ...site,
    allowed_path_prefixes: ['/'],
    auto_login_enabled: true,
    login_url: 'https://example.com/login',
    username_selector: '#username',
    password_selector: '#password',
    submit_selector: 'button[type=submit]',
  }, { origin: 'https://example.com', username: 'me', password: 'secret' });
  assert.equal(result.status, 'auto_logged_in');
  assert.deepEqual(fills, [['#username', 'me'], ['#password', 'secret']]);
  assert.equal(closed, 'example');
});

test('autoLogin stops when credential origin differs', async () => {
  const sessions = {
    calls: 0,
    pageFor() { this.calls += 1; return { page: fakePage(), entry: {} }; },
    get() { return {}; },
    async close() { return true; },
    async closeAll() {},
  };
  const collector = new BrowserCollector({ sessions, captureItem: async () => {}, now: () => 1000 });
  await assert.rejects(
    collector.autoLogin({
      ...site,
      allowed_path_prefixes: ['/'],
      auto_login_enabled: true,
      login_url: 'https://example.com/login',
      username_selector: '#u',
      password_selector: '#p',
      submit_selector: '#s',
    }, { origin: 'https://evil.example.com', username: 'u', password: 'p' }),
    /origin/,
  );
  assert.equal(sessions.calls, 0);
});

test('collector persists schedule and page checkpoints across restart', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-browser-'));
  try {
    const statePath = path.join(dir, 'browser-state.json');
    const items = [];
    let now = 1000;
    const first = new BrowserCollector({
      sessions: fakeSessions(),
      captureItem: async item => items.push(item),
      now: () => now,
      statePath,
    });
    const result = await first.runSite(site, { timeoutMs: 5000 });
    assert.equal(result.items, 1);
    assert.equal(items.length, 1);
    const saved = JSON.parse(fs.readFileSync(statePath, 'utf8'));
    assert.equal(saved.sites.example.lastRunAt, 1000);
    assert.ok(saved.sites.example.pages['https://example.com/items/1'].hash);

    now = 2000;
    const restored = new BrowserCollector({
      sessions: fakeSessions(),
      captureItem: async item => items.push(item),
      now: () => now,
      statePath,
    });
    assert.equal(restored.statusFor('example').lastRunAt, 1000);
    assert.equal((await restored.schedulerTick([site])).length, 0, 'restart must not immediately recrawl');

    now = 70_000;
    const unchanged = await restored.runSite(site, { timeoutMs: 5000 });
    assert.equal(unchanged.items, 0);
    assert.equal(unchanged.unchanged, 1);
    assert.equal(items.length, 1);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('collector follows only authorized links within page limit', async () => {
  const items = [];
  const sessions = fakeSessions({
    links: [
      { text: 'allowed', href: 'https://example.com/items/2' },
      { text: 'foreign', href: 'https://evil.example.com/items/3' },
    ],
  });
  const collector = new BrowserCollector({
    sessions,
    captureItem: async item => items.push(item),
    now: () => 1000,
  });
  const result = await collector.runSite({ ...site, navigation_enabled: true }, { timeoutMs: 5000 });
  assert.equal(result.items, 2);
  assert.equal(items.length, 2);
  assert.equal(sessions.calls.length, 2);
  assert.ok(sessions.calls.every(url => url.startsWith('https://example.com/items/')));
});
