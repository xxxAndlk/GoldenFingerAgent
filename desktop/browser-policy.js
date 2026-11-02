'use strict';

// 网页采集的 URL 权限与边界检查。只允许 http(s)，不允许跳转到未授权 origin。
function normalizeUrl(raw) {
  let value = String(raw || '').trim();
  if (!value) throw new Error('URL 不能为空');
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error('URL 格式无效');
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error('只支持 http(s) URL');
  }
  parsed.hash = '';
  const query = [...parsed.searchParams.entries()]
    .sort(([a, av], [b, bv]) => (a === b ? av.localeCompare(bv) : a.localeCompare(b)));
  parsed.search = '';
  for (const [key, value] of query) parsed.searchParams.append(key, value);
  return parsed;
}

function canonicalUrl(raw) {
  const parsed = normalizeUrl(raw);
  let pathname = parsed.pathname || '/';
  if (pathname.length > 1 && pathname.endsWith('/')) pathname = pathname.replace(/\/+$/, '/');
  parsed.pathname = pathname;
  return parsed.toString();
}

function urlOrigin(raw) {
  const parsed = normalizeUrl(raw);
  return `${parsed.protocol}//${parsed.host}`.toLowerCase();
}

function originAllowed(origin, allowedOrigins) {
  const normalized = String(origin || '').toLowerCase().replace(/\/+$/, '');
  return (allowedOrigins || []).some(value =>
    String(value || '').toLowerCase().replace(/\/+$/, '') === normalized);
}

function pathAllowed(pathname, prefixes) {
  if (!prefixes || !prefixes.length) return true;
  return prefixes.some(raw => {
    let prefix = String(raw || '').trim();
    if (!prefix) return false;
    if (!prefix.startsWith('/')) prefix = `/${prefix}`;
    if (pathname === prefix || pathname.startsWith(prefix.endsWith('/') ? prefix : `${prefix}/`)) {
      return true;
    }
    return prefix.endsWith('/') && pathname.startsWith(prefix);
  });
}

function checkUrl(raw, site) {
  const canonical = canonicalUrl(raw);
  const parsed = new URL(canonical);
  const origins = (site && site.allowed_origins) || [];
  const prefixes = (site && site.allowed_path_prefixes) || [];
  const origin = `${parsed.protocol}//${parsed.host}`.toLowerCase();
  if (!originAllowed(origin, origins)) {
    throw new Error(`origin ${origin} 不在授权范围`);
  }
  if (!pathAllowed(parsed.pathname, prefixes)) {
    throw new Error(`path ${parsed.pathname} 不在授权范围`);
  }
  return canonical;
}

function sanitizeSiteLimits(site) {
  const interval = Number(site && site.interval_seconds) || 1800;
  const pages = Number(site && site.max_pages) || 5;
  const depth = Number(site && site.max_depth) || 0;
  return {
    intervalSeconds: Math.min(86400, Math.max(60, interval)),
    maxPages: Math.min(20, Math.max(1, pages)),
    maxDepth: Math.min(3, Math.max(0, depth)),
  };
}

function siteReady(site) {
  return Boolean(site && site.enabled && Array.isArray(site.start_urls) && site.start_urls.length);
}

function deriveOrigins(startUrls) {
  const seen = new Set();
  const out = [];
  for (const raw of startUrls || []) {
    try {
      const origin = urlOrigin(raw);
      if (!seen.has(origin)) {
        seen.add(origin);
        out.push(origin);
      }
    } catch {
      // 无效 URL 留给设置接口拒绝。
    }
  }
  return out;
}

module.exports = {
  normalizeUrl,
  canonicalUrl,
  urlOrigin,
  originAllowed,
  pathAllowed,
  checkUrl,
  sanitizeSiteLimits,
  siteReady,
  deriveOrigins,
};
