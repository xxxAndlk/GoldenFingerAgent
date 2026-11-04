'use strict';

const crypto = require('node:crypto');
const { redactSensitiveContent } = require('./redaction');

function normalizeText(value) {
  return String(value || '').replace(/\s+/g, ' ').trim();
}

function contentHash(text) {
  return crypto.createHash('sha256').update(normalizeText(text), 'utf8').digest('hex');
}

function readabilityPath() {
  return require.resolve('@mozilla/readability/Readability.js');
}

// 在页面上下文内运行 Readability；失败时退回 DOM 正文。返回内容仍按不可信数据处理。
async function extractPage(page, options = {}) {
  try {
    await page.addScriptTag({ path: readabilityPath() });
  } catch {
    // 页面 CSP 或脚本错误时继续走 DOM 兜底。
  }
  const extracted = await page.evaluate(() => {
    let article = null;
    try {
      if (typeof Readability === 'function') {
        article = new Readability(document.cloneNode(true), { charThreshold: 80 }).parse();
      }
    } catch {
      article = null;
    }
    const meta = name => {
      const node = document.querySelector(`meta[name="${name}"], meta[property="${name}"]`);
      return node ? node.getAttribute('content') || '' : '';
    };
    const bodyText = document.body ? document.body.innerText : '';
    const readableText = article && article.textContent ? article.textContent : '';
    const useReadable = readableText.trim().length >= 200;
    const links = Array.from(document.querySelectorAll('a[href]'))
      .slice(0, 200)
      .map(node => ({ text: (node.innerText || '').trim().slice(0, 160), href: node.href }))
      .filter(link => link.href);
    return {
      title: (article && article.title) || document.title || '',
      text: useReadable ? readableText : bodyText,
      byline: (article && article.byline) || meta('author'),
      siteName: (article && article.siteName) || meta('og:site_name'),
      publishedTime: (article && article.publishedTime) || meta('article:published_time'),
      lang: document.documentElement.lang || '',
      excerpt: (article && article.excerpt) || meta('description'),
      url: location.href,
      extractedBy: useReadable ? 'readability' : 'dom',
      completeness: useReadable ? 'complete' : (bodyText.trim().length > 0 ? 'partial' : 'unknown'),
      links,
    };
  });
  const redacted = redactSensitiveContent(extracted.text);
  return {
    ...extracted,
    text: redacted.text,
    redacted: redacted.redacted,
    canonicalUrl: options.canonicalUrl || extracted.url,
    capturedAt: new Date().toISOString(),
  };
}

function makeWebItem({ site, account, url, extraction, change = 'new' }) {
  const text = normalizeText(extraction.text);
  if (!text) throw new Error('页面没有可读取的正文');
  const canonical = extraction.canonicalUrl || url;
  return {
    site_id: site.id,
    account: account || site.account || site.id,
    url,
    canonical_url: canonical,
    item_id: extraction.itemId || canonical,
    title: String(extraction.title || '').trim().slice(0, 2000),
    text,
    captured_at: extraction.capturedAt || new Date().toISOString(),
    extracted_by: extraction.extractedBy || 'dom',
    completeness: extraction.completeness || 'unknown',
    change,
    evidence_ids: Array.isArray(extraction.evidenceIds) ? extraction.evidenceIds.slice(0, 50) : [],
  };
}

module.exports = { normalizeText, contentHash, extractPage, makeWebItem };
