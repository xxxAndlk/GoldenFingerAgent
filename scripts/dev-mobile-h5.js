'use strict';

// 手机版 H5 调试：复用 cmd/server 与 web/，但数据库、设置和上传文件全部隔离。
// Kimi Work 可通过 npm run dev -- --host 127.0.0.1 --port 7100 启停预览。
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const args = process.argv.slice(2);

function option(name, fallback) {
  const prefix = `--${name}=`;
  const inline = args.find(arg => arg.startsWith(prefix));
  if (inline) return inline.slice(prefix.length);
  const index = args.indexOf(`--${name}`);
  if (index < 0) return fallback;
  const value = args[index + 1];
  return value && !value.startsWith('--') ? value : fallback;
}

const host = option('host', '127.0.0.1');
const port = Number(option('port', '7100'));
if (!['127.0.0.1', 'localhost', '::1'].includes(host)) {
  throw new Error('H5 调试服务没有完整鉴权，只允许监听 127.0.0.1、localhost 或 ::1');
}
if (!Number.isInteger(port) || port < 1 || port > 65535) {
  throw new Error(`无效端口：${port}`);
}

const dataDir = path.resolve(root, process.env.GFA_H5_DATA_DIR || path.join('artifacts', 'mobile-h5-dev'));
fs.mkdirSync(dataDir, { recursive: true });

const projectGo = path.join(root, '.bma', 'tools', 'go', 'bin', process.platform === 'win32' ? 'go.exe' : 'go');
const go = process.env.GFA_GO || (fs.existsSync(projectGo) ? projectGo : 'go');
const listenHost = host === '::1' ? '[::1]' : host;
const child = spawn(go, ['run', './cmd/server'], {
  cwd: root,
  env: {
    ...process.env,
    GFA_CONFIG: process.env.GFA_H5_CONFIG || path.join(root, 'config.yaml'),
    GFA_SETTINGS: path.join(dataDir, 'settings.json'),
    GFA_DATABASE_PATH: path.join(dataDir, 'gfa.db'),
    GFA_SERVER_ADDR: `${listenHost}:${port}`,
    // 默认不在预览后台抽取长期记忆；需要完整链路时可显式设为 0。
    GFA_MEMORY_DISABLE: process.env.GFA_MEMORY_DISABLE || '1',
  },
  stdio: 'inherit',
  windowsHide: true,
});

let stopping = false;
function stop(signal = 'SIGTERM') {
  if (stopping || !child.pid) return;
  stopping = true;
  if (process.platform === 'win32') {
    // go run 会再启动一个服务子进程，必须结束整棵进程树。
    const killer = spawn('taskkill', ['/pid', String(child.pid), '/t', '/f'], { stdio: 'ignore', windowsHide: true });
    killer.once('error', () => child.kill());
  } else {
    child.kill(signal);
  }
}

process.on('SIGINT', () => stop('SIGINT'));
process.on('SIGTERM', () => stop('SIGTERM'));
process.on('disconnect', () => stop());
child.once('error', error => {
  console.error(`无法启动 Go 服务：${error.message}`);
  process.exitCode = 1;
});
child.once('exit', (code, signal) => {
  if (!stopping && code !== 0) {
    console.error(`H5 调试服务退出：code=${code} signal=${signal || ''}`);
  }
  process.exitCode = stopping ? 0 : (code || 0);
});

console.log(`手机版 H5：http://${listenHost}:${port}/`);
console.log(`隔离数据：${dataDir}（不会读写 data/gfa.db 或根目录 settings.json）`);
