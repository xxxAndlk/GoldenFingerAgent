'use strict';

const crypto = require('node:crypto');

function clean(value, length = 240) {
  return String(value || '').replace(/\s+/g, ' ').trim().slice(0, length);
}

function normalizeContentLine(value) {
  return clean(value, 1000)
    .replace(/(?<=[\u3400-\u9fff])\s+(?=[\u3400-\u9fff])/g, '')
    .normalize('NFKC');
}

function contentKey(value) {
  return normalizeContentLine(value).replace(/[\s\p{P}\p{S}]/gu, '').toLowerCase();
}

function observationHash(value) {
  return crypto.createHash('sha256').update(contentKey(value)).digest('hex');
}

// 所有采集源都先转换成这个结构，再由正式轨迹或影子评估器消费。
// evidenceRef 是不透明的本地证据引用，不进入长期记忆或公开 UI 事件。
function normalizeObservation(event, defaults = {}) {
  const content = String(event.content || '').trim().slice(0, 8000);
  const at = Number(event.at || defaults.at || Date.now());
  const title = clean(event.title || event.activeTab, 240);
  return {
    id: event.id || '',
    type: clean(event.type || 'content', 40),
    at,
    app: clean(event.app, 120) || '未知应用',
    title,
    windowTitle: clean(event.windowTitle, 240),
    activeTab: clean(event.activeTab || title, 300),
    url: clean(event.url, 1000),
    detail: clean(event.detail, 300),
    content,
    contentHash: clean(event.contentHash, 128) || observationHash(content || event.detail || title),
    evidenceRef: clean(event.evidenceRef || event.evidencePath, 2000),
    source: clean(event.source || defaults.source || 'unknown', 80),
    privacyLevel: event.privacyLevel === 'protected' ? 'protected' : 'normal',
    skipEvidence: Boolean(event.skipEvidence),
    captureNote: clean(event.captureNote, 300),
  };
}

function selectNovelContent(text, seenLines) {
  const novel = [];
  for (const rawLine of String(text || '').split('\n')) {
    const line = normalizeContentLine(rawLine);
    const key = contentKey(line);
    if (key.length < 4) continue;
    if (seenLines.size >= 4000) seenLines.delete(seenLines.values().next().value);
    if (!seenLines.has(key) && novel.join('\n').length < 1800) novel.push(line);
    seenLines.add(key);
  }
  return novel.join('\n').slice(0, 1800);
}

function contentExcerpt(text) {
  const lines = String(text || '').split('\n').map(line => normalizeContentLine(line)).filter(Boolean);
  const meaningful = lines.filter(line => (line.match(/[\p{L}\p{N}]/gu) || []).length >= 6 && !/^https?:/i.test(line));
  const candidates = meaningful.length ? meaningful : lines;
  const ranked = candidates.map((line, index) => {
    const letters = (line.match(/[\p{L}\p{N}]/gu) || []).length;
    const score = Math.min(letters, 180) + (/^https?:/i.test(line) ? -120 : 0) + (letters < 6 ? -80 : 0);
    return { line, index, score };
  }).sort((a, b) => b.score - a.score).slice(0, 3).sort((a, b) => a.index - b.index);
  return clean(ranked.map(item => item.line).join('；'), 180);
}

// ActivitySource 合约：reset()、observe({force}) 和 stop()。observe 返回
// 标准 Observation 或 null；实现不得直接写正式轨迹、聊天或长期记忆。
class ActivitySource {
  reset() {}
  async observe() { throw new Error('ActivitySource.observe 未实现'); }
  stop() {}
}

module.exports = {
  ActivitySource,
  clean,
  contentKey,
  observationHash,
  normalizeObservation,
  selectNovelContent,
  contentExcerpt,
};
