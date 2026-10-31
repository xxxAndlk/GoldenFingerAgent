'use strict';

const fs = require('node:fs');
const path = require('node:path');

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
  'window-capture.js', 'git-connector.js', 'server-manager.js', 'window-dock.js', 'activity-format.js',
  'index.html', 'styles.css', 'activity.html', 'activity.css', 'activity-renderer.js',
  'settings-renderer.js', 'agents.html', 'management.js', 'ccswitch.html', 'ccswitch.js', 'windows.css',
  'markdown.js', 'agent-panel.js',
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
  fs.copyFileSync(path.join(root, file), path.join(appOutput, file));
}
const projectRoot = path.resolve(root, '..');
const serverOutput = path.join(appOutput, 'server');
fs.mkdirSync(serverOutput, { recursive: true });
fs.copyFileSync(path.join(projectRoot, 'server-dist', 'goldenfinger-server.exe'), path.join(serverOutput, 'goldenfinger-server.exe'));
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
console.log(`DESKTOP_PACKAGE_OK ${output}`);
