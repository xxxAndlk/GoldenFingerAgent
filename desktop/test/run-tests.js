'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const testDir = __dirname;
const files = fs.readdirSync(testDir)
  .filter(name => name.endsWith('.test.js'))
  .map(name => path.join(testDir, name));
files.push(path.join(__dirname, '..', 'scripts', 'test-input-monitor.js'));

const result = spawnSync(process.execPath, ['--test', ...files], { stdio: 'inherit' });
process.exit(result.status ?? 1);
