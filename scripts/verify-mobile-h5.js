'use strict';

// 用模拟 Responses / Embeddings 服务验证 H5，不消耗真实模型额度。
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { spawn } = require('node:child_process');
const { chromium } = require('../desktop/node_modules/playwright-core');

const root = path.resolve(__dirname, '..');
const dataDir = path.join(root, 'artifacts', 'mobile-h5-verify');
const outputDir = path.join(dataDir, 'screenshots');
const port = 7100;
const base = `http://127.0.0.1:${port}`;
function devLaunch() {
  const args = ['--host', '127.0.0.1', '--port', String(port)];
  if (process.env.npm_execpath) {
    return [process.execPath, [process.env.npm_execpath, 'run', 'dev', '--', ...args]];
  }
  return [process.execPath, [path.join(root, 'scripts', 'dev-mobile-h5.js'), ...args]];
}
const edge = process.env.GFA_BROWSER || (process.platform === 'win32'
  ? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
  : 'chromium');

function yamlPath(value) {
  return JSON.stringify(String(value).replace(/\\/g, '/'));
}

function listen(server, port = 0) {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => resolve(server.address().port));
  });
}

function closeServer(server) {
  return new Promise(resolve => server.close(resolve));
}

function stopTree(child) {
  if (!child || !child.pid || child.exitCode !== null) return Promise.resolve();
  return new Promise(resolve => {
    const timer = setTimeout(resolve, 5000);
    child.once('exit', () => { clearTimeout(timer); resolve(); });
    if (process.platform === 'win32') {
      spawn('taskkill', ['/pid', String(child.pid), '/t', '/f'], { stdio: 'ignore', windowsHide: true })
        .once('error', () => child.kill());
    } else {
      child.kill('SIGTERM');
    }
  });
}

async function waitHealth(child) {
  const deadline = Date.now() + 45000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`H5 服务提前退出：${child.exitCode}`);
    try {
      const response = await fetch(`${base}/api/me`, { signal: AbortSignal.timeout(1000) });
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  throw new Error('等待 H5 服务就绪超时');
}

async function main() {
  fs.rmSync(dataDir, { recursive: true, force: true });
  fs.mkdirSync(outputDir, { recursive: true });
  try {
    const existing = await fetch(`${base}/api/me`, { signal: AbortSignal.timeout(800) });
    if (existing.ok) throw new Error(`端口 ${port} 已有服务，停止后再运行验证`);
  } catch (error) {
    if (error.message.includes('已有服务')) throw error;
  }

  let responses = 0;
  const mock = http.createServer((req, res) => {
    req.resume();
    req.once('end', () => {
      res.setHeader('Content-Type', 'application/json; charset=utf-8');
      if (req.method === 'POST' && req.url === '/responses') {
        responses++;
        res.end(JSON.stringify({
          id: `resp_h5_${responses}`, status: 'completed',
          output: [{ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'H5_BROWSER_OK' }] }],
          usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 },
        }));
        return;
      }
      if (req.method === 'POST' && req.url === '/v1/embeddings') {
        res.end(JSON.stringify({ object: 'list', data: [{ object: 'embedding', index: 0, embedding: [0.1, 0.2, 0.3] }], model: 'h5-mock', usage: { prompt_tokens: 1, total_tokens: 1 } }));
        return;
      }
      res.statusCode = 404;
      res.end(JSON.stringify({ error: 'not found' }));
    });
  });
  const mockPort = await listen(mock);
  const config = path.join(dataDir, 'config.yaml');
  const mockBase = `http://127.0.0.1:${mockPort}`;
  fs.writeFileSync(config, [
    'server:',
    `  addr: "127.0.0.1:${port}"`,
    `  web_dir: ${yamlPath(path.join(root, 'web'))}`,
    'database:',
    `  path: ${yamlPath(path.join(dataDir, 'gfa.db'))}`,
    'llm:',
    `  base_url: ${JSON.stringify(mockBase)}`,
    '  api_key: "h5-mock"',
    '  model: "h5-mock"',
    'embedder:',
    `  base_url: ${JSON.stringify(`${mockBase}/v1`)}`,
    '  api_key: "h5-mock"',
    '  model: "h5-mock"',
    '  dim: 3',
    'digest:',
    '  time: "23:59"',
    '',
  ].join('\n'), { mode: 0o600 });

  let dev;
  let browser;
  try {
    dev = spawn(...devLaunch(), {
      cwd: root,
      env: { ...process.env, GFA_H5_CONFIG: config, GFA_H5_DATA_DIR: dataDir },
      stdio: 'inherit',
      windowsHide: true,
    });
    await waitHealth(dev);
    browser = await chromium.launch({ executablePath: edge, headless: true });
    const context = await browser.newContext({
      viewport: { width: 390, height: 844 },
      deviceScaleFactor: 1,
      isMobile: true,
      hasTouch: true,
    });
    const page = await context.newPage();
    const pageErrors = [];
    page.on('pageerror', error => pageErrors.push(error.message));

    await page.goto(base, { waitUntil: 'networkidle' });
    await page.getByText('已连接本机管家', { exact: true }).waitFor();
    await page.screenshot({ path: path.join(outputDir, 'home.png'), fullPage: true });
    const initialMetrics = await page.evaluate(() => ({
      width: innerWidth,
      height: innerHeight,
      scrollWidth: document.documentElement.scrollWidth,
      inputVisible: Boolean(document.querySelector('#inputbar input')?.getBoundingClientRect().width),
    }));
    if (initialMetrics.width !== 390 || initialMetrics.height !== 844 || !initialMetrics.inputVisible) {
      throw new Error(`移动视口或输入区异常：${JSON.stringify(initialMetrics)}`);
    }
    if (initialMetrics.scrollWidth > initialMetrics.width) {
      throw new Error(`页面横向溢出：${JSON.stringify(initialMetrics)}`);
    }

    await page.getByRole('button', { name: '设置', exact: true }).click();
    await page.getByText('云端共享记忆', { exact: true }).click();
    await page.getByText(/本机模式/).first().waitFor();
    await page.screenshot({ path: path.join(outputDir, 'settings.png'), fullPage: true });
    await page.locator('aside.drawer:visible button[aria-label="关闭"]').click();

    const input = page.getByPlaceholder('有什么需要我帮忙的吗？');
    await input.fill('验证手机 H5 消息发送');
    await page.getByRole('button', { name: '发送', exact: true }).click();
    await page.getByText('H5_BROWSER_OK', { exact: true }).waitFor({ timeout: 20000 });
    await page.screenshot({ path: path.join(outputDir, 'chat.png'), fullPage: true });

    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('H5_BROWSER_OK', { exact: true }).waitFor();
    await page.getByRole('button', { name: '对话历史', exact: true }).click();
    await page.locator('aside.drawer:visible').waitFor();
    if (pageErrors.length) throw new Error(`浏览器脚本错误：${pageErrors.join(' | ')}`);
    if (responses < 1) throw new Error(`模拟模型调用次数异常：${responses}（发送消息至少应调用一次）`);

    console.log(`H5_VERIFY_OK viewport=390x844 settings=ok chat=mock persistence=ok history=ok horizontalOverflow=false pageErrors=0 responses=${responses}`);
    console.log(`H5_VERIFY_SCREENSHOTS ${outputDir}`);
  } finally {
    if (browser) await browser.close();
    await stopTree(dev);
    await closeServer(mock);
  }
}

main().catch(error => {
  console.error(error.stack || error.message);
  process.exitCode = 1;
});
