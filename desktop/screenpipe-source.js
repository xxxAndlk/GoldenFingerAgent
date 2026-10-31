'use strict';

const { ActivitySource, clean, normalizeObservation, observationHash } = require('./observation');

const CAPTURE_TYPES = ['accessibility', 'ocr', 'input'];

function screenpipePrivacy(app, title) {
  const value = `${app} ${title}`;
  return /1password|keepass|bitwarden|密码|支付|银行|隐身|incognito|inprivate/i.test(value) ? 'protected' : 'normal';
}

function parseTimestamp(value) {
  if (typeof value === 'number') return Number.isFinite(value) ? value : NaN;
  return Date.parse(value || '');
}

function parseScreenpipeItem(item, fallbackType = '') {
  if (!item || typeof item !== 'object') return null;
  const body = item.content && typeof item.content === 'object' ? item.content : item;
  const rawType = String(item.type || body.type || fallbackType).toLowerCase();
  const type = rawType.includes('input') ? 'input' : rawType.includes('ocr') ? 'ocr' : 'accessibility';
  if (item.focused === false || body.focused === false) return null;
  const app = clean(body.app_name || body.appName || item.app_name, 120);
  const title = clean(body.window_name || body.windowName || item.window_name, 240);
  if (/goldenfinger|金手指管家|工作轨迹/i.test(`${app} ${title}`)) return null;
  const timestamp = parseTimestamp(body.timestamp || item.timestamp);
  const at = Number.isFinite(timestamp) ? timestamp : Date.now();
  const url = clean(body.browser_url || body.browserUrl || item.browser_url, 1000);
  const activeTab = clean(body.active_tab || body.browser_title || body.tab_title || title, 300);
  const frameID = body.frame_id ?? body.frameId ?? item.frame_id;
  const evidenceRef = frameID === undefined || frameID === null || frameID === '' ? '' : `screenpipe:frame:${frameID}`;
  if (type === 'input') {
    const eventType = clean(body.event_type || body.eventType || body.key, 60).toLowerCase();
    const text = clean(body.text || body.value || body.input, 1800);
    if (!text && !/click|app_switch|window_focus/.test(eventType)) return null;
    return normalizeObservation({
      type: text ? 'submission' : 'action', app, title, windowTitle: title, activeTab, url,
      content: text, detail: text ? `提交：${clean(text, 180)}` : clean(eventType, 180),
      evidenceRef, source: 'screenpipe:input', at,
      privacyLevel: screenpipePrivacy(app, title),
    });
  }
  const text = String(body.text || body.ocr_text || body.accessibility_text || '').trim().slice(0, 8000);
  if (!text) return null;
  return normalizeObservation({
    type: 'content', app, title: activeTab || title, windowTitle: title, activeTab, url,
    content: text, detail: clean(text, 180), evidenceRef,
    source: `screenpipe:${type}`, at, privacyLevel: screenpipePrivacy(app, title),
  });
}

class ScreenpipeSource extends ActivitySource {
  constructor({ baseUrl = 'http://127.0.0.1:3030', apiKey = '', fetchImpl = fetch, now = () => Date.now(), lookbackMs = 15000 }) {
    super();
    this.baseUrl = String(baseUrl).replace(/\/$/, '');
    this.apiKey = apiKey;
    this.fetch = fetchImpl;
    this.now = now;
    this.lookbackMs = lookbackMs;
    this.reset();
  }

  reset() {
    this.cursor = this.now() - this.lookbackMs;
    this.seen = new Set();
  }

  async request(path) {
    const headers = { Accept: 'application/json', 'X-Screenpipe-Client': 'api' };
    if (this.apiKey) headers.Authorization = `Bearer ${this.apiKey}`;
    const response = await this.fetch(`${this.baseUrl}${path}`, { headers, signal: AbortSignal.timeout(4000) });
    if (!response.ok) throw new Error(`Screenpipe HTTP ${response.status}`);
    return response.json();
  }

  async health() {
    return this.request('/health');
  }

  async search(type, start, end) {
    const query = new URLSearchParams({
      limit: '100', content_type: type,
      start_time: new Date(start).toISOString(), end_time: new Date(end).toISOString(),
    });
    const response = await this.request(`/search?${query}`);
    return Array.isArray(response) ? response : Array.isArray(response.data) ? response.data : [];
  }

  remember(key) {
    if (this.seen.size >= 4000) this.seen.delete(this.seen.values().next().value);
    if (this.seen.has(key)) return false;
    this.seen.add(key);
    return true;
  }

  async poll() {
    const end = this.now();
    const start = Math.max(0, this.cursor - 1000);
    let received = 0;
    let duplicates = 0;
    let latest = this.cursor;
    const observations = [];
    for (const type of CAPTURE_TYPES) {
      const items = await this.search(type, start, end);
      for (const item of items) {
        received += 1;
        const body = item && item.content && typeof item.content === 'object' ? item.content : item;
        const timestamp = parseTimestamp(body && (body.timestamp || item.timestamp));
        if (Number.isFinite(timestamp)) latest = Math.max(latest, timestamp);
        const observation = parseScreenpipeItem(item, type);
        if (!observation || observation.at < this.cursor) continue;
        const identity = observationHash(`${observation.app}\0${observation.windowTitle}\0${observation.type}\0${observation.content || observation.detail}`);
        if (!this.remember(identity)) {
          duplicates += 1;
          continue;
        }
        observations.push(observation);
      }
    }
    this.cursor = Math.max(this.cursor, latest, end - 1000);
    return { observations, received, duplicates };
  }
}

module.exports = { ScreenpipeSource, parseScreenpipeItem, screenpipePrivacy, CAPTURE_TYPES };
