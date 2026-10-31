'use strict';

function clean(value, length = 1000) {
  return String(value || '').replace(/\s+/g, ' ').trim().slice(0, length);
}

function normalizeContentLine(value) {
  return clean(value)
    .replace(/(?<=[\u3400-\u9fff])\s+(?=[\u3400-\u9fff])/g, '')
    .normalize('NFKC');
}

function contentKey(value) {
  return normalizeContentLine(value).replace(/[\s\p{P}\p{S}]/gu, '').toLowerCase();
}

function grams(value, size = 3) {
  const result = new Set();
  if (value.length < size) return result;
  for (let index = 0; index <= value.length - size; index += 1) result.add(value.slice(index, index + size));
  return result;
}

function numberSignature(value) {
  return (value.match(/\d+(?:\.\d+)?/g) || []).join('|');
}

function similarEnough(left, right) {
  if (left.key === right.key) return true;
  if (left.key.length < 16 || right.key.length < 16) return false;
  if (Math.min(left.key.length, right.key.length) / Math.max(left.key.length, right.key.length) < 0.84) return false;
  if (numberSignature(left.key) !== numberSignature(right.key)) return false;
  let intersection = 0;
  for (const gram of left.grams) if (right.grams.has(gram)) intersection += 1;
  const containment = intersection / Math.max(1, Math.min(left.grams.size, right.grams.size));
  const dice = 2 * intersection / Math.max(1, left.grams.size + right.grams.size);
  return containment >= 0.76 && dice >= 0.74;
}

class ContentMemory {
  constructor(limit = 4000) {
    this.limit = limit;
    this.entries = new Map();
    this.gramIndex = new Map();
  }

  has(line, key = contentKey(line)) {
    if (this.entries.has(key)) return true;
    const candidate = { key, grams: grams(key) };
    const possible = new Set();
    for (const gram of candidate.grams) {
      for (const existing of this.gramIndex.get(gram) || []) possible.add(existing);
    }
    for (const existing of possible) {
      const entry = this.entries.get(existing);
      if (entry && similarEnough(candidate, entry)) return true;
    }
    return false;
  }

  remember(line) {
    const normalized = normalizeContentLine(line);
    const key = contentKey(normalized);
    if (key.length < 4 || this.entries.has(key)) return key;
    const entry = { key, line: normalized, grams: grams(key) };
    if (this.entries.size >= this.limit) this.evict(this.entries.keys().next().value);
    this.entries.set(key, entry);
    for (const gram of entry.grams) {
      if (!this.gramIndex.has(gram)) this.gramIndex.set(gram, new Set());
      this.gramIndex.get(gram).add(key);
    }
    return key;
  }

  evict(key) {
    const entry = this.entries.get(key);
    if (!entry) return;
    this.entries.delete(key);
    for (const gram of entry.grams) {
      const keys = this.gramIndex.get(gram);
      if (!keys) continue;
      keys.delete(key);
      if (!keys.size) this.gramIndex.delete(gram);
    }
  }

  add(text) {
    for (const line of String(text || '').split('\n')) this.remember(line);
  }

  select(text, maxLength = 1800) {
    const novel = [];
    for (const rawLine of String(text || '').split('\n')) {
      const line = normalizeContentLine(rawLine);
      const key = contentKey(line);
      if (key.length < 4) continue;
      if (!this.has(line, key) && novel.join('\n').length < maxLength) novel.push(line);
      this.remember(line);
    }
    return novel.join('\n').slice(0, maxLength);
  }
}

module.exports = { ContentMemory, normalizeContentLine, contentKey, similarEnough };
