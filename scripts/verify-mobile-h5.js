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
const port = Number(process.env.GFA_H5_VERIFY_PORT || 7100);
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

// assertNoOverflow 检查当前视口没有横向溢出。
async function assertNoOverflow(page, label) {
  const metrics = await page.evaluate(() => ({
    width: innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  if (metrics.scrollWidth > metrics.width) {
    throw new Error(`${label} 横向溢出：${JSON.stringify(metrics)}`);
  }
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
    // 先建一个长期任务，让任务中心有真实数据可看（不启动执行，避免额外模型调用）。
    const created = await fetch(`${base}/api/work-tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: '验证用长期任务',
        goal: '检查手机端任务中心能否看到长期任务',
        profile: 'internal',
        // queued 才能验证暂停/继续；start=false 不启动真实执行。
        status: 'queued',
        start: false,
      }),
    });
    if (!created.ok) {
      throw new Error(`创建验证用长期任务失败：${created.status} ${await created.text()}`);
    }
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

    // ---- 顶部入口：收件箱换成每日简报，通知功能仍在设置里 ----
    const headerLabels = await page.locator('header button').evaluateAll(
      nodes => nodes.map(n => n.getAttribute('aria-label'))
    );
    if (headerLabels.includes('收件箱')) throw new Error(`顶部仍存在收件箱入口：${headerLabels.join(',')}`);
    if (!headerLabels.includes('每日简报')) throw new Error(`顶部缺少每日简报入口：${headerLabels.join(',')}`);
    if (!headerLabels.includes('任务中心')) throw new Error(`顶部缺少任务中心入口：${headerLabels.join(',')}`);

    await page.locator('aside.drawer:visible button[aria-label="关闭"]').click();

    // ---- 每日简报：Markdown 渲染，不是 <pre> 纯文本 ----
    await page.getByRole('button', { name: '每日简报', exact: true }).click();
    await page.locator('#brief-panel:visible').waitFor();
    await page.locator('#brief-panel').getByRole('button', { name: '重新生成', exact: true }).click();
    await page.locator('#brief-panel .brief-body').waitFor({ timeout: 30000 });
    const briefPre = await page.locator('#brief-panel .brief-body pre').count();
    if (briefPre > 0) throw new Error('每日简报仍用 <pre> 输出 Markdown 源码');
    const briefMd = await page.locator('#brief-panel .brief-body .md').count();
    if (briefMd < 1) throw new Error('每日简报正文没有走 Markdown 渲染');
    await page.screenshot({ path: path.join(outputDir, 'brief.png'), fullPage: true });
    await assertNoOverflow(page, '每日简报抽屉');
    await page.locator('aside.drawer:visible button[aria-label="关闭"]').click();

    // ---- 会话目录：主对话 / 工作任务 / 委派 Agent 三类可切换 ----
    // 注入一条带真实会话、且后端标记为可写的委派记录：前端必须强制只读，
    // 不能再渲染一个「发了没反应」的输入框。
    const mainList = await (await fetch(`${base}/api/conversations?kind=main&limit=1`)).json();
    const mainSessionId = (mainList.conversations || [])[0]?.session_id;
    if (!mainSessionId) throw new Error('主会话缺少 session_id，无法验证委派只读');
    await page.route(/\/api\/conversations\?kind=delegated/, async route => {
      const response = await route.fetch();
      const body = await response.json();
      body.conversations = [{
        id: 'delegated-verify',
        kind: 'delegated',
        session_id: mainSessionId,
        title: '验证委派执行',
        status: 'running',
        can_message: true,
        agent_id: 'bma',
      }, ...(body.conversations || [])];
      await route.fulfill({ response, json: body });
    });
    const delegatedMessagePosts = [];
    const trackDelegated = request => {
      if (request.method() === 'POST' && request.url().includes('/messages')) {
        delegatedMessagePosts.push(request.url());
      }
    };
    page.on('request', trackDelegated);
    await page.getByRole('button', { name: '对话历史', exact: true }).click();
    await page.locator('#history-panel:visible').waitFor();
    await page.locator('#history-panel').getByRole('button', { name: '工作任务', exact: true }).click();
    await page.locator('#history-panel').getByRole('button', { name: '委派 Agent', exact: true }).click();
    await page.locator('#history-panel .conv-item-title', { hasText: '验证委派执行' }).click();
    await page.locator('#worker-panel:visible').waitFor({ timeout: 10000 });
    const composeCount = await page.locator('#worker-panel .worker-compose').count();
    if (composeCount !== 0) throw new Error('委派执行记录仍渲染发送框');
    const workerInputs = await page.locator('#worker-panel input').count();
    if (workerInputs !== 0) throw new Error('委派执行记录仍有可输入控件');
    const workerHints = await page.locator('#worker-panel .worker-hint').allTextContents();
    if (!workerHints.some(text => text.includes('这里只读'))) {
      throw new Error(`委派执行记录缺少只读提示：${workerHints.join(' | ')}`);
    }
    page.off('request', trackDelegated);
    if (delegatedMessagePosts.length) {
      throw new Error(`委派只读记录发出了消息请求：${delegatedMessagePosts.join(',')}`);
    }
    await page.locator('#worker-panel').locator('button[aria-label="关闭"]').click();
    // 打开 Worker 面板会收起会话目录，切回主对话前要重新打开。
    await page.getByRole('button', { name: '对话历史', exact: true }).click();
    await page.locator('#history-panel:visible').waitFor();
    await page.locator('#history-panel').getByRole('button', { name: '主对话', exact: true }).click();
    await assertNoOverflow(page, '会话目录');
    await page.locator('aside.drawer:visible button[aria-label="关闭"]').click();

    // ---- 任务中心：待你处理 / 执行中 / 已完成 ----
    await page.getByRole('button', { name: '任务中心', exact: true }).click();
    await page.locator('#tasks-panel:visible').waitFor();
    await page.locator('#tasks-panel .conv-item-title', { hasText: '验证用长期任务' })
      .waitFor({ timeout: 15000 });
    const taskTitles = await page.locator('#tasks-panel .conv-item-title').allTextContents();
    if (!taskTitles.some(t => t.includes('验证用长期任务'))) {
      throw new Error(`任务中心没有显示刚创建的任务：${taskTitles.join(' | ')}`);
    }
    // 等抽屉滑入动画结束，截图才是稳定态。
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(outputDir, 'tasks.png'), fullPage: true });
    await assertNoOverflow(page, '任务中心');

    // ---- 任务动作必须打到长期任务接口，而不是被同名旧提醒方法顶掉 ----
    const taskPosts = [];
    page.on('request', request => {
      if (request.method() !== 'POST') return;
      const url = new URL(request.url());
      if (url.pathname.startsWith('/api/')) taskPosts.push(url.pathname);
    });
    await page.locator('#tasks-panel .conv-item-title', { hasText: '验证用长期任务' }).click();
    await page.locator('#task-detail-panel:visible').waitFor({ timeout: 15000 });
    await page.locator('#task-detail-panel').getByRole('button', { name: '暂停', exact: true })
      .click({ timeout: 10000 });
    await page.waitForFunction(() => {
      const buttons = Array.from(document.querySelectorAll('#task-detail-panel button'));
      return buttons.some(b => b.textContent.trim() === '继续');
    }, null, { timeout: 15000 });
    if (!taskPosts.some(p => p.includes('/api/work-tasks/') && p.endsWith('/pause'))) {
      throw new Error(`任务暂停没有打到长期任务接口：${taskPosts.join(',') || '(无请求)'}`);
    }
    if (taskPosts.some(p => p.startsWith('/api/tasks/'))) {
      throw new Error(`任务操作打到了旧提醒接口：${taskPosts.join(',')}`);
    }
    await page.locator('#task-detail-panel').getByRole('button', { name: '继续', exact: true }).click();
    await page.waitForFunction(() => {
      const buttons = Array.from(document.querySelectorAll('#task-detail-panel button'));
      return buttons.some(b => b.textContent.trim() === '暂停');
    }, null, { timeout: 15000 });
    if (!taskPosts.some(p => p.includes('/api/work-tasks/') && p.endsWith('/resume'))) {
      throw new Error(`任务继续没有打到长期任务接口：${taskPosts.join(',')}`);
    }
    // 任务详情叠在任务中心之上，两层都要关掉。
    await page.locator('#task-detail-panel').locator('button[aria-label="关闭"]').click();
    await page.locator('#tasks-panel').locator('button[aria-label="关闭"]').click();

    // ---- 通知功能仍在设置里可进入 ----
    await page.getByRole('button', { name: '设置', exact: true }).click();
    await page.locator('#settings-panel:visible').waitFor();
    await page.locator('#settings-panel').getByText('渠道采集与每日简报', { exact: true }).click();
    await page.locator('#settings-panel .channel-state').first().waitFor({ timeout: 10000 });
    const availability = await page.locator('#settings-panel .channel-state').allTextContents();
    for (const expected of ['本机可用', '需电脑在线', '未接入']) {
      if (!availability.some(text => text.includes(expected))) {
        throw new Error(`渠道三态缺少「${expected}」：${availability.join(' | ')}`);
      }
    }
    const disabledUnsupported = await page.locator('#settings-panel .channel-state.unsupported').count();
    if (disabledUnsupported < 1) {
      throw new Error(`未接入渠道没有标成 unsupported：${availability.join(' | ')}`);
    }
    // 渠道开关必须是能点的原生 checkbox，不能被 .field input 的样式压成一个空方块。
    const switchBox = await page.locator('#settings-panel .chan-check input').first().evaluate(el => {
      const rect = el.getBoundingClientRect();
      const style = getComputedStyle(el);
      return { width: rect.width, height: rect.height, appearance: style.appearance };
    });
    if (switchBox.width < 14 || switchBox.height < 14) {
      throw new Error(`渠道开关尺寸异常：${JSON.stringify(switchBox)}`);
    }
    if (switchBox.appearance === 'none') {
      throw new Error(`渠道开关没有恢复原生外观：${JSON.stringify(switchBox)}`);
    }
    await assertNoOverflow(page, '设置抽屉');
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(outputDir, 'channels.png'), fullPage: true });
    await page.locator('#settings-panel').getByRole('button', { name: '打开通知收件箱' }).click();
    await page.locator('#inbox-panel:visible').waitFor();
    await page.locator('#inbox-panel').getByRole('button', { name: '稍后处理', exact: true }).waitFor();
    await page.locator('#inbox-panel').getByRole('button', { name: '线索', exact: true }).waitFor();
    await page.locator('#inbox-panel').getByRole('button', { name: '等待中', exact: true }).waitFor();
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(outputDir, 'inbox.png'), fullPage: true });

    // ---- 通知点击路由：容器调用 gfaOpenRoute 能打开对应面板，未知路由不猜页面 ----
    await page.locator('#inbox-panel').locator('button[aria-label="关闭"]').click();
    const routed = await page.evaluate(() => window.gfaOpenRoute && window.gfaOpenRoute('tasks'));
    if (routed !== 'tasks') throw new Error(`通知路由没有打开任务中心：${routed}`);
    await page.locator('#tasks-panel:visible').waitFor({ timeout: 10000 });
    await page.locator('#tasks-panel').locator('button[aria-label="关闭"]').click();
    const unknownRoute = await page.evaluate(() => window.gfaOpenRoute('no-such-panel'));
    if (unknownRoute !== 'unknown') throw new Error(`未知路由不应猜页面：${unknownRoute}`);

    if (pageErrors.length) throw new Error(`浏览器脚本错误：${pageErrors.join(' | ')}`);

    console.log(`H5_VERIFY_OK viewport=390x844 settings=ok chat=mock persistence=ok history=ok delegated=readonly brief=markdown taskCenter=ok taskActions=work-tasks inbox=ok headerBrief=ok channels=tri-state route=ok horizontalOverflow=false pageErrors=0 responses=${responses}`);
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
