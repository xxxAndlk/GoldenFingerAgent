'use strict';

// OCR/UIA 可能读到编辑器里的密钥；轨迹只保留脱敏文本，含密钥的截图直接丢弃。
const LONG_TOKEN = /\b[A-Za-z0-9_-]{32,}\b/;
const SENSITIVE_LABEL = /\b(?:api[\s_-]?keys?|access[\s_-]?token|auth(?:orization)?|bearer|token|secret|password|passwd|pwd|private[\s_-]?key)\b|密\s*钥/i;
const BARE_LABEL = /^\s*["']?(?:api[\s_-]?keys?|access[\s_-]?token|auth(?:orization)?|bearer|token|secret|password|passwd|pwd|private[\s_-]?key|密\s*钥)["']?\s*:?\s*$/i;
const KEY_LIKE = /(?:sk[\s_-]|[A-Za-z0-9][A-Za-z0-9\s_.:\/+-]{20,})/i;
const RULES = [
  {
    test: /\bsk-[A-Za-z0-9_-]{8,}\b/i,
    replace: [/\bsk-[A-Za-z0-9_-]{8,}\b/gi, '[已隐藏密钥]'],
  },
  {
    test: /\b(?:gh[pousr]_[A-Za-z0-9]{10,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{8,})\b/i,
    replace: [/\b(?:gh[pousr]_[A-Za-z0-9]{10,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{8,})\b/gi, '[已隐藏密钥]'],
  },
  {
    test: /\bBearer\s+[A-Za-z0-9._~+/=-]{12,}/i,
    replace: [/(\bBearer\s+)[A-Za-z0-9._~+/=-]{12,}/gi, '$1[已隐藏]'],
  },
  {
    test: /\b(?:api[_\s-]?key|access[_\s-]?token|auth(?:orization)?|token|secret|password|passwd|pwd)\b\s*[:=]\s*["']?[^\s"',;]{4,}/i,
    replace: [/(\b(?:api[_\s-]?key|access[_\s-]?token|auth(?:orization)?|token|secret|password|passwd|pwd)\b\s*[:=]\s*)(["']?)[^\s"',;]{4,}/gi, '$1$2[已隐藏]'],
  },
  {
    test: LONG_TOKEN,
    replace: [/\b[A-Za-z0-9_-]{32,}\b/g, '[已隐藏长串]'],
  },
];

function containsSensitiveContent(value) {
  const text = String(value || '');
  return SENSITIVE_LABEL.test(text) || RULES.some(rule => rule.test.test(text));
}

function redactSensitiveContent(value) {
  let text = String(value || '');
  let redacted = false;
  for (const rule of RULES) {
    if (!rule.test.test(text)) continue;
    redacted = true;
    text = text.replace(rule.replace[0], rule.replace[1]);
  }
  // OCR 常把 JSON 拆成两行：上一行只有 "api_key"，下一行才是值。
  const lines = text.split('\n');
  let afterLabel = false;
  for (let index = 0; index < lines.length; index += 1) {
    if (BARE_LABEL.test(lines[index])) {
      redacted = true;
      lines[index] = '[已隐藏敏感标签]';
      afterLabel = true;
      continue;
    }
    if (afterLabel && KEY_LIKE.test(lines[index])) {
      redacted = true;
      lines[index] = '[已隐藏]';
    }
    afterLabel = false;
  }
  return { text: lines.join('\n'), redacted };
}

module.exports = { containsSensitiveContent, redactSensitiveContent };
