'use strict';

const fs = require('node:fs');
const path = require('node:path');

if (process.platform !== 'win32') {
  throw new Error('Windows 桌面包只能在 Windows 上生成');
}

const root = path.resolve(__dirname, '..');
const electronDist = path.join(root, 'node_modules', 'electron', 'dist');
const output = path.join(root, 'dist', 'GoldenFinger-win');
const appOutput = path.join(output, 'resources', 'app');
const files = [
  'main.js', 'preload.js', 'renderer.js', 'assistant-api.js',
  'collaboration.js', 'active-window.js', 'index.html', 'styles.css',
];

if (!fs.existsSync(path.join(electronDist, 'electron.exe'))) {
  throw new Error('Electron 运行文件缺失，请先执行 npm install');
}
fs.rmSync(output, { recursive: true, force: true });
fs.cpSync(electronDist, output, { recursive: true });
fs.rmSync(path.join(output, 'resources', 'default_app.asar'), { recursive: true, force: true });
fs.mkdirSync(appOutput, { recursive: true });
for (const file of files) {
  fs.copyFileSync(path.join(root, file), path.join(appOutput, file));
}
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
fs.writeFileSync(path.join(appOutput, 'package.json'), JSON.stringify({
  name: pkg.name,
  version: pkg.version,
  private: true,
  main: 'main.js',
}, null, 2));
fs.renameSync(path.join(output, 'electron.exe'), path.join(output, 'GoldenFinger.exe'));
console.log(`DESKTOP_PACKAGE_OK ${output}`);
