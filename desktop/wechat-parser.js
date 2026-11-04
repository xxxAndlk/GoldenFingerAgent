'use strict';

const { contentHash, eventId, messageKey } = require('./observation-envelope');

const TIME_LABEL = /^(?:\d{1,2}[:：]\d{2}|\d{1,2}\s*[/月-]\s*\d{1,2}(?:日)?|(?:星期|周)[一二三四五六日天]|昨天|今天|前天)$/;
const UI_LABEL = /^(?:发送|发消息|按住说话|搜索|聊天信息|语音通话|视频通话|更多|表情|文件|图片|截图)$/;

function normalizeLine(value) {
  let text = String(value || '').replace(/\s+/g, ' ').trim().normalize('NFKC');
  // Windows OCR 常在汉字之间插空格；只去掉汉字之间的空格，保留英文单词间距。
  text = text.replace(/(?<=[㐀-鿿])\s+(?=[㐀-鿿])/g, '');
  text = text.replace(/\s+([，。！？；：、）】》」』])/g, '$1');
  text = text.replace(/([（【《「『])\s+/g, '$1');
  return text.trim();
}

function contentKey(value) {
  return normalizeLine(value).replace(/[\s\p{P}\p{S}]/gu, '').toLowerCase();
}

function plausibleMessage(text) {
  const key = contentKey(text);
  if (!key) return false;
  if (TIME_LABEL.test(text) || UI_LABEL.test(text)) return false;
  if (/^\d{1,2}\s*[:：]\s*\d{2}$/.test(text)) return false;
  return /[\p{L}\p{N}]/u.test(key);
}

function unionBox(boxes) {
  const left = Math.min(...boxes.map(box => box.x));
  const top = Math.min(...boxes.map(box => box.y));
  const right = Math.max(...boxes.map(box => box.x + box.width));
  const bottom = Math.max(...boxes.map(box => box.y + box.height));
  return { x: left, y: top, width: right - left, height: bottom - top };
}

function median(values) {
  const sorted = values.filter(Number.isFinite).sort((a, b) => a - b);
  if (!sorted.length) return 0;
  return sorted[Math.floor(sorted.length / 2)];
}

function boxDirection(box, width, text) {
  const right = box.x + box.width;
  const center = box.x + box.width / 2;
  if (Math.abs(center - width / 2) < width * 0.06 && /^(?:已|你已|对方|该|撤回|加入|退出)/.test(text)) {
    return 'system';
  }
  return right >= width * 0.82 ? 'self' : 'other';
}

function isGroupTitle(title) {
  return /[（(]\s*\d+\s*[）)]\s*$/.test(String(title || ''));
}

// 从 Windows OCR 的带坐标行中解析微信当前会话。无法取得稳定会话 ID 时只用标题作为
// source_id，并在健康状态中明确这是当前可见内容，不伪装成完整历史。
function parseWeChatChat({ boxes = [], width = 0, height = 0, now = new Date() } = {}) {
  const imageWidth = Number(width);
  const imageHeight = Number(height);
  if (!Number.isFinite(imageWidth) || !Number.isFinite(imageHeight) || imageWidth < 100 || imageHeight < 100) {
    return { title: '', messages: [], completeness: 'unknown', error: '截图尺寸无效' };
  }
  const paneLeft = imageWidth * 0.33;
  const topLimit = imageHeight * 0.085;
  const bottomLimit = imageHeight * 0.78;
  const normalized = [];
  for (const box of boxes || []) {
    const text = normalizeLine(box && box.text);
    if (!text || !plausibleMessage(text)) continue;
    const x = Number(box.x);
    const y = Number(box.y);
    const w = Number(box.width);
    const h = Number(box.height);
    if (![x, y, w, h].every(Number.isFinite) || w <= 0 || h <= 0) continue;
    normalized.push({ text, x, y, width: w, height: h });
  }

  const titleBox = normalized
    .filter(box => box.x >= paneLeft && box.y < topLimit)
    .sort((a, b) => a.y - b.y || a.x - b.x)[0];
  const title = titleBox ? titleBox.text : '';

  const candidates = normalized
    .filter(box => box.x >= paneLeft && box.y >= topLimit && box.y < bottomLimit)
    .map(box => ({ ...box, direction: boxDirection(box, imageWidth, box.text) }))
    .sort((a, b) => a.y - b.y || a.x - b.x);

  const medianHeight = median(candidates.map(box => box.height)) || 24;
  const mergeGap = Math.max(12, medianHeight * 0.8);
  const merged = [];
  for (const box of candidates) {
    const last = merged[merged.length - 1];
    if (last && last.direction === box.direction && box.y - (last.box.y + last.box.height) <= mergeGap) {
      last.lines.push(box);
      last.box = unionBox(last.lines);
      continue;
    }
    merged.push({ lines: [box], box: { ...box }, direction: box.direction });
  }

  const observedAt = (now instanceof Date ? now : new Date(now)).toISOString();
  const group = isGroupTitle(title);
  const messages = merged.map((entry, index) => {
    const text = entry.lines.map(line => line.text).join('\n').trim();
    const direction = entry.direction;
    const sender = direction === 'self' ? '我'
      : direction === 'system' ? 'system'
        : (group ? '' : title);
    return {
      sequence: index,
      text,
      direction,
      sender,
      sender_id: '',
      message_type: 'visible_text',
      observed_at: observedAt,
      occurred_at: null,
      completeness: 'current_visible',
      content_hash: contentHash(text),
      box: entry.box,
    };
  }).filter(message => message.text);

  return {
    title,
    source_id: title ? `wechat-uia:${title}` : '',
    display_name: title,
    is_group: group,
    messages,
    completeness: messages.length ? 'current_visible' : 'unknown',
    pane: { left: paneLeft, top: topLimit, bottom: bottomLimit },
  };
}

function similarity(left, right) {
  const a = contentKey(left);
  const b = contentKey(right);
  if (!a && !b) return 1;
  if (!a || !b) return 0;
  if (a === b) return 1;
  if (a.length >= 4 && b.length >= 4 && (a.includes(b) || b.includes(a))) {
    return Math.min(a.length, b.length) / Math.max(a.length, b.length);
  }
  if (Math.max(a.length, b.length) > 160) {
    const grams = value => new Set(Array.from({ length: Math.max(0, value.length - 1) }, (_, index) => value.slice(index, index + 2)));
    const ga = grams(a);
    const gb = grams(b);
    let hit = 0;
    for (const value of ga) if (gb.has(value)) hit += 1;
    return (2 * hit) / Math.max(1, ga.size + gb.size);
  }
  const rows = a.length + 1;
  const cols = b.length + 1;
  let previous = Array.from({ length: cols }, (_, index) => index);
  for (let i = 1; i < rows; i += 1) {
    const current = [i];
    for (let j = 1; j < cols; j += 1) {
      current[j] = Math.min(
        previous[j] + 1,
        current[j - 1] + 1,
        previous[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1),
      );
    }
    previous = current;
  }
  return 1 - previous[cols - 1] / Math.max(a.length, b.length);
}

// 当前可见窗口通常包含上次的部分历史。用后缀/前缀模糊对齐，识别新增消息。
// 连续相同文本保留为独立条目，不按内容去重。
function alignVisibleMessages(previous, current, threshold = 0.7) {
  const prev = Array.isArray(previous) ? previous : [];
  const curr = Array.isArray(current) ? current : [];
  if (!prev.length) {
    return { aligned: true, overlap: 0, gap: false, messages: curr.map(message => ({ ...message, event_id: eventId(), key: messageKey(message.text, message.direction) })) };
  }
  for (let k = Math.min(prev.length, curr.length); k >= 1; k -= 1) {
    let matched = true;
    for (let index = 0; index < k; index += 1) {
      const before = prev[prev.length - k + index];
      const after = curr[index];
      if (!before || !after || before.direction !== after.direction || similarity(before.text, after.text) < threshold) {
        matched = false;
        break;
      }
    }
    if (matched) {
      const messages = curr.map((message, index) => {
        if (index < k) {
          const before = prev[prev.length - k + index];
          return { ...message, event_id: before.event_id || eventId(), key: messageKey(message.text, message.direction) };
        }
        return { ...message, event_id: eventId(), key: messageKey(message.text, message.direction) };
      });
      return { aligned: true, overlap: k, gap: false, messages };
    }
  }
  return {
    aligned: false,
    overlap: 0,
    gap: true,
    messages: curr.map(message => ({ ...message, event_id: eventId(), key: messageKey(message.text, message.direction) })),
  };
}

module.exports = {
  parseWeChatChat,
  alignVisibleMessages,
  normalizeLine,
  contentKey,
  similarity,
  plausibleMessage,
  isGroupTitle,
};
