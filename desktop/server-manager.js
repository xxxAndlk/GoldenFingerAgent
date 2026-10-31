'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');

function yamlPath(value) {
  return `"${String(value).replace(/\\/g, '/').replace(/"/g, '\\"')}"`;
}

class ServerManager {
  constructor({ appDir, userData, baseUrl = 'http://127.0.0.1:8080', addr = process.env.GFA_SERVER_ADDR || '127.0.0.1:8080', spawnImpl = spawn, fetchImpl = fetch }) {
    this.appDir = appDir;
    this.userData = userData;
    this.baseUrl = baseUrl;
    // 监听地址：设为 0.0.0.0:8080 时手机可通过局域网 IP 直接访问同一服务。
    this.addr = addr;
    this.spawn = spawnImpl;
    this.fetch = fetchImpl;
    this.child = null;
    this.starting = null;
  }

  async healthy() {
    try {
      const response = await this.fetch(`${this.baseUrl}/api/settings/llm`, { signal: AbortSignal.timeout(1500) });
      return response.ok;
    } catch {
      return false;
    }
  }

  prepare() {
    fs.mkdirSync(this.userData, { recursive: true });
    const settings = path.join(this.userData, 'settings.json');
    const legacyRoot = path.resolve(this.appDir, '..', '..', '..', '..', '..');
    const legacyConfig = path.join(legacyRoot, 'config.yaml');
    const legacySettings = path.join(legacyRoot, 'settings.json');
    if (!fs.existsSync(settings) && fs.existsSync(legacySettings)) fs.copyFileSync(legacySettings, settings);
    if (fs.existsSync(legacyConfig)) {
      return { config: legacyConfig, settings, cwd: legacyRoot };
    }
    const config = path.join(this.userData, 'server-config.yaml');
    const webDir = path.join(this.appDir, 'server', 'web');
    const content = `server:\n  addr: ${yamlPath(this.addr)}\n  web_dir: ${yamlPath(webDir)}\ndatabase:\n  path: ${yamlPath(path.join(this.userData, 'goldenfinger.db'))}\nllm:\n  base_url: ""\n  api_key: ""\n  model: ""\nembedder:\n  base_url: ""\n  api_key: ""\n  model: ""\n  dim: 3\n`;
    fs.writeFileSync(config, content, { mode: 0o600 });
    return { config, settings, cwd: this.userData };
  }

  async ensure() {
    if (await this.healthy()) return { connected: true, managed: false };
    if (this.starting) return this.starting;
    this.starting = this.start();
    try { return await this.starting; }
    finally { this.starting = null; }
  }

  async start() {
    const executable = path.join(this.appDir, 'server', 'goldenfinger-server.exe');
    if (!fs.existsSync(executable)) throw new Error('本机服务组件缺失，请重新解压完整桌面版');
    const prepared = this.prepare();
    const log = fs.openSync(path.join(this.userData, 'server.log'), 'a');
    this.child = this.spawn(executable, [], {
      cwd: prepared.cwd,
      env: {
        ...process.env,
        GFA_CONFIG: prepared.config,
        GFA_SETTINGS: prepared.settings,
      },
      windowsHide: true,
      stdio: ['ignore', log, log],
    });
    fs.closeSync(log);
    this.child.once('exit', () => { this.child = null; });
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
      if (await this.healthy()) return { connected: true, managed: true };
      if (!this.child) break;
      await new Promise(resolve => setTimeout(resolve, 250));
    }
    this.stop();
    throw new Error(`本机服务启动失败，请查看 ${path.join(this.userData, 'server.log')}`);
  }

  stop() {
    if (!this.child) return;
    const child = this.child;
    this.child = null;
    child.kill();
  }
}

module.exports = { ServerManager };
