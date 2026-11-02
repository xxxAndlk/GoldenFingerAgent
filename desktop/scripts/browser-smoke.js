'use strict';

// 开源组件冒烟：用本机 Edge/Chrome 打开本地 HTML，验证 Playwright + Readability。
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright-core');
const { BrowserSessionManager } = require('../browser-session');
const { extractPage } = require('../browser-extract');

const html = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>本地样例</title></head>
<body><nav>导航噪声</nav><article>
<h1>本地采集样例</h1>
<p>这是用于验证 Playwright 与 Readability 的本地正文。它不访问真实网站。为了触发 Readability 正文提取，这一段需要足够长，并包含多个完整句子。</p>
<p>通用网页采集需要读取标题、正文、链接和页面元数据，去除导航、广告、脚本与重复页脚。未专门适配的网站也应该能完成正文提取和来源回溯，同时明确标记提取方式与完整性。</p>
<p>第三段继续补充内容，确保正文长度超过 Readability 的阈值。第二段包含一个链接 <a href="https://example.com/items/2">条目 2</a>。</p>
</article><footer>页脚噪声</footer></body></html>`;

(async () => {
  const rootDir = path.join(os.tmpdir(), `gfa-browser-smoke-${Date.now()}`);
  const manager = new BrowserSessionManager({ rootDir, chromium, logger: console });
  try {
    const { page } = await manager.pageFor({ id: 'smoke', account: 'local' }, 'about:blank', { headless: true });
    await page.setContent(html, { waitUntil: 'domcontentloaded' });
    const result = await extractPage(page, { canonicalUrl: 'https://example.com/items/1' });
    if (!result.text.includes('本地正文')) throw new Error('正文提取失败');
    console.log(JSON.stringify({
      ok: true,
      title: result.title,
      extractedBy: result.extractedBy,
      completeness: result.completeness,
      textLength: result.text.length,
      linkCount: result.links.length,
    }));
  } finally {
    await manager.closeAll();
  }
})().catch(error => {
  console.error(JSON.stringify({ ok: false, error: error.message }));
  process.exitCode = 1;
});
