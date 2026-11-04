'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

if (process.platform !== 'win32') {
  throw new Error('Windows 桌面包只能在 Windows 上生成');
}

const root = path.resolve(__dirname, '..');
const electronDist = path.join(root, 'node_modules', 'electron', 'dist');
const output = path.join(root, 'dist', process.env.GFA_DIST_DIR || 'GoldenFinger-win');
const appOutput = path.join(output, 'resources', 'app');
const files = [
  'main.js', 'preload.js', 'renderer.js', 'assistant-api.js',
  'collaboration.js', 'active-window.js', 'activity-trail.js', 'input-monitor.js',
  'observation.js', 'native-activity-source.js', 'screenpipe-source.js', 'screenpipe-shadow.js',
  'ui-context.js', 'page-content.js', 'content-dedup.js', 'screen-ocr.js', 'visual-change.js', 'desktop-content.js',
  'window-capture.js', 'screen-capture.js', 'redaction.js', 'git-connector.js', 'server-manager.js', 'window-dock.js', 'activity-format.js', 'activity-summary.js',
  'index.html', 'styles.css', 'activity.html', 'activity.css', 'activity-renderer.js',
  'activity-handle.html', 'activity-handle.css', 'activity-handle.js',
  'settings-renderer.js', 'agents.html', 'management.js', 'ccswitch.html', 'ccswitch.js', 'windows.css',
  'markdown.js', 'agent-panel.js', 'workbench.html', 'workbench.js',
  'browser-policy.js', 'browser-extract.js', 'browser-session.js', 'browser-collector.js',
  'secret-store.js', 'observation-envelope.js', 'observation-spool.js',
  'wechat-db-connector.js', 'connector-requests.js',
  'tray-icon.png', 'tray-icon.ico', 'tray-icon-busy.png', 'tray-icon-busy.ico',
  'app-icon.png', 'app-icon.ico', 'app-icon.css',
];

if (!fs.existsSync(path.join(electronDist, 'electron.exe'))) {
  throw new Error('Electron 运行文件缺失，请先执行 npm install');
}
fs.rmSync(output, { recursive: true, force: true });
fs.cpSync(electronDist, output, { recursive: true });
// resources/app 存在时 Electron 会优先启动本应用，无需删除只读的默认应用包。
fs.mkdirSync(appOutput, { recursive: true });
for (const file of files) {
  const target = path.join(appOutput, file);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.copyFileSync(path.join(root, file), target);
}
// 只读数据库连接器是独立 Go 可执行文件，不再带 Python 运行时。
const wechatConnector = path.join(root, '..', 'server-dist', 'wechat-db-connector.exe');
if (!fs.existsSync(wechatConnector)) {
  throw new Error('缺少 server-dist/wechat-db-connector.exe，请先构建 Go 连接器');
}
const connectorTarget = path.join(appOutput, 'connectors', 'wechat-db', 'wechat-db-connector.exe');
fs.mkdirSync(path.dirname(connectorTarget), { recursive: true });
fs.copyFileSync(wechatConnector, connectorTarget);
try {
  execFileSync('icacls', [connectorTarget, '/setintegritylevel', 'M'], { windowsHide: true, stdio: 'ignore' });
} catch { /* 非管理员环境下保留原标签，运行时会明确报权限错误。 */ }
// 网页采集依赖 playwright-core 与 Readability；只复制这两个运行时依赖，不带 devDependencies。
for (const moduleName of ['playwright-core', '@mozilla/readability']) {
  const source = path.join(root, 'node_modules', moduleName);
  if (fs.existsSync(source)) {
    const target = path.join(appOutput, 'node_modules', moduleName);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.cpSync(source, target, { recursive: true });
  }
}
const projectRoot = path.resolve(root, '..');
const serverOutput = path.join(appOutput, 'server');
fs.mkdirSync(serverOutput, { recursive: true });
const serverTarget = path.join(serverOutput, 'goldenfinger-server.exe');
fs.copyFileSync(path.join(projectRoot, 'server-dist', 'goldenfinger-server.exe'), serverTarget);
// Electron 主进程与服务端都必须以 Medium 完整性运行：Low 标签下 Chromium 沙箱
// 无法初始化（启动即静默退出），Low 进程也读不到微信进程内存。
try {
  execFileSync('icacls', [serverTarget, '/setintegritylevel', 'M'], { windowsHide: true, stdio: 'ignore' });
} catch { /* 非管理员环境下保留原标签，运行时会明确报权限错误。 */ }
fs.cpSync(path.join(projectRoot, 'web'), path.join(serverOutput, 'web'), { recursive: true });
fs.cpSync(path.join(projectRoot, 'skills'), path.join(serverOutput, 'skills'), { recursive: true });
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
fs.writeFileSync(path.join(appOutput, 'package.json'), JSON.stringify({
  name: pkg.name,
  version: pkg.version,
  private: true,
  main: 'main.js',
}, null, 2));
fs.renameSync(path.join(output, 'electron.exe'), path.join(output, 'GoldenFinger.exe'));
// 主进程可执行文件必须带 Medium 标签：Low 标签下 Chromium 沙箱无法初始化，
// 表现为启动后立即静默退出（退出码 3），没有任何日志。
try {
  execFileSync('icacls', [path.join(output, 'GoldenFinger.exe'), '/setintegritylevel', 'M'], { windowsHide: true, stdio: 'ignore' });
} catch { /* 非管理员环境下保留原标签，运行时会明确报权限错误。 */ }
console.log(`DESKTOP_PACKAGE_OK ${output}`);
