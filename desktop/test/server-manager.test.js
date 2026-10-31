'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
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

test('ServerManager reports a clear connection state', async () => {
  const manager = new ServerManager({ appDir: 'D:\\unused', userData: 'D:\\unused', fetchImpl: async () => { throw new Error('offline'); } });
  assert.equal(await manager.healthy(), false);
});
