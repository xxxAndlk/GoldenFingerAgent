'use strict';

const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { spawn } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const temp = path.join(root, '.bma', 'model-smoke');
fs.rmSync(temp, { recursive: true, force: true });
fs.mkdirSync(temp, { recursive: true });
const settingsPath = path.join(temp, 'settings.json');
const configPath = path.join(temp, 'config.yaml');
fs.writeFileSync(configPath, `server:\n  addr: "127.0.0.1:18080"\n  web_dir: "${path.join(root, 'web').replace(/\\/g, '/')}"\ndatabase:\n  path: "${path.join(temp, 'smoke.db').replace(/\\/g, '/')}"\nllm:\n  base_url: "http://127.0.0.1:18081"\n  model: "fallback-model"\nembedder:\n  base_url: "http://127.0.0.1:18081"\n  model: "embed-model"\n  dim: 3\n`);

let lastChatModel = '';
const fake = http.createServer((req, res) => {
  let body = '';
  req.on('data', chunk => { body += chunk; });
  req.on('end', () => {
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/chat/completions') {
      lastChatModel = JSON.parse(body).model;
      res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: '模型连接成功' }, finish_reason: 'stop' }] }));
      return;
    }
    if (req.url === '/embeddings') {
      const input = JSON.parse(body).input || [];
      res.end(JSON.stringify({ data: input.map(() => ({ embedding: [0.1, 0.2, 0.3] })) }));
      return;
    }
    res.statusCode = 404;
    res.end(JSON.stringify({ error: 'wrong protocol path' }));
  });
});

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function request(pathname, options) {
  const response = await fetch(`http://127.0.0.1:18080${pathname}`, options);
  if (!response.ok) throw new Error(`${response.status}: ${await response.text()}`);
  return response.json();
}

(async () => {
  await new Promise(resolve => fake.listen(18081, '127.0.0.1', resolve));
  const server = spawn(path.join(root, 'server-dist', 'goldenfinger-server.exe'), [], {
    cwd: root,
    env: { ...process.env, GFA_CONFIG: configPath, GFA_SETTINGS: settingsPath },
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let logs = '';
  server.stdout.on('data', data => { logs += data; });
  server.stderr.on('data', data => { logs += data; });
  try {
    let ready = false;
    for (let i = 0; i < 50; i += 1) {
      try { await request('/api/settings/llm'); ready = true; break; } catch { await sleep(200); }
    }
    if (!ready) throw new Error(`服务未启动：${logs}`);
    await request('/api/settings/llm', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ base_url: 'http://127.0.0.1:18081', model: 'saved-model', api_key: '', protocol: 'chat_completions' }),
    });
    const saved = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
    if (saved.llm.protocol !== 'chat_completions') throw new Error('JSON 未保存协议');
    saved.llm.model = 'hot-model';
    fs.writeFileSync(settingsPath, JSON.stringify(saved, null, 2));
    const current = await request('/api/settings/llm');
    if (current.model !== 'hot-model') throw new Error('外部 JSON 修改未热更新');
    const chat = await request('/api/chat', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text: '测试连接' }),
    });
    if (chat.reply !== '模型连接成功' || lastChatModel !== 'hot-model') throw new Error(`模型调用失败：${JSON.stringify(chat)} ${logs}`);
    console.log('MODEL_CONFIG_SMOKE_OK json=persisted hot_reload=true protocol=chat_completions chat=ok');
  } finally {
    server.kill();
    fake.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
