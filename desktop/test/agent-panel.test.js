'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { canDelegate, canOpen, visibleAgents } = require('../agent-panel');

test('Agent panel only enables real invocation channels', () => {
  assert.equal(canDelegate({ capabilities: ['delegate'] }), true);
  assert.equal(canDelegate({ capabilities: ['scan', 'desktop_delegate'] }), true);
  assert.equal(canDelegate({ capabilities: ['scan', 'quota'] }), false);
  assert.equal(canDelegate({}), false);
});

test('Installed desktop agents can open before they are running', () => {
  assert.equal(canOpen({ kind: 'desktop', capabilities: ['scan', 'open'] }), true);
  assert.equal(canOpen({ kind: 'desktop', capabilities: ['scan'] }), false);
  assert.equal(canOpen({ kind: 'cli', capabilities: ['open', 'delegate'] }), true);
  assert.equal(canOpen({ kind: 'cli', capabilities: ['delegate'] }), false);
});

test('Agent list hides entries the scan did not find', () => {
  const agents = [
    { id: 'installed-desktop', kind: 'desktop', installed: true },
    { id: 'missing-desktop', kind: 'desktop', installed: false },
    { id: 'running-desktop', kind: 'desktop', installed: false, running: true },
    { id: 'installed-cli', kind: 'cli', installed: true },
    { id: 'bma', kind: 'bma', installed: true },
  ];
  assert.deepEqual(visibleAgents(agents, 'desktop').map(agent => agent.id), ['installed-desktop', 'running-desktop']);
  assert.deepEqual(visibleAgents(agents, 'cli').map(agent => agent.id), ['installed-cli']);
  assert.deepEqual(visibleAgents(agents, 'service').map(agent => agent.id), ['bma']);
});
