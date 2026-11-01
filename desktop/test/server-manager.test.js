'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { ServerManager } = require('../server-manager');

test('ServerManager reuses an already healthy local service', async () => {
  let spawned = false;
  const manager = new ServerManager({
    appDir: 'D:\\unused', userData: 'D:\\unused',
    fetchImpl: async () => ({ ok: true }),
    spawnImpl: () => { spawned = true; },
  });
  const result = await manager.ensure();
  assert.equal(result.connected, true);
  assert.equal(result.managed, false);
  assert.equal(spawned, false);
});

test('ServerManager keeps config and settings together when a legacy project root exists', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-server-manager-'));
  fs.writeFileSync(path.join(root, 'config.yaml'), 'server: {}\n');
  fs.writeFileSync(path.join(root, 'settings.json'), '{}\n');
  const appDir = path.join(root, 'desktop', 'dist', 'GoldenFinger-win', 'resources', 'app');
  const manager = new ServerManager({ appDir, userData: path.join(root, 'userdata') });
  const prepared = manager.prepare();
  assert.equal(prepared.config, path.join(root, 'config.yaml'));
  assert.equal(prepared.settings, path.join(root, 'settings.json'));
  assert.equal(prepared.cwd, root);
});

test('ServerManager reports a clear connection state', async () => {
  const manager = new ServerManager({ appDir: 'D:\\unused', userData: 'D:\\unused', fetchImpl: async () => { throw new Error('offline'); } });
  assert.equal(await manager.healthy(), false);
});
