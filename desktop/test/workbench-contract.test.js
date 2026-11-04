'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function read(name) {
  return fs.readFileSync(path.join(__dirname, '..', name), 'utf8');
}

test('Workbench contract wires WorkTask, artifact and grant through preload and IPC', () => {
  const preload = read('preload.js');
  const main = read('main.js');
  const workbench = read('workbench.js');
  const html = read('workbench.html');
  const index = read('index.html');
  const renderer = read('renderer.js');

  for (const method of [
    'listWorkTasks', 'getWorkTask', 'createWorkTask', 'messageWorkTask',
    'pauseWorkTask', 'resumeWorkTask', 'cancelWorkTask', 'recoverWorkTask',
    'reopenWorkTask', 'completeWorkTask', 'createArtifact', 'submitArtifact', 'acceptArtifact', 'rejectArtifact',
    'createTaskGrant', 'revokeGrant',
  ]) {
    assert.match(preload, new RegExp(method), `preload missing ${method}`);
    assert.match(workbench, new RegExp(method), `workbench missing ${method}`);
  }
  for (const channel of [
    'assistant:work-tasks', 'assistant:work-task-get', 'assistant:work-task-create',
    'assistant:work-task-message', 'assistant:work-task-action', 'assistant:artifact-create',
    'assistant:artifact-accept', 'assistant:artifact-reject', 'assistant:task-grant-create',
  ]) {
    assert.match(main, new RegExp(channel), `main missing ${channel}`);
  }
  assert.match(html, /panel-tasks/);
  assert.match(html, /data-tab="tasks"/);
  assert.match(html, /data-tab="access"/);
  assert.match(html, /panel-access/);
  assert.doesNotMatch(workbench, /记录补充（当前版本不会实时注入）/);
  assert.match(workbench, /追加指令/);
  assert.match(workbench, /工作台怎么用/);
  assert.match(index, /workbench-label/);
  assert.match(index, /M6 6l12 12M18 6L6 18/);
  assert.doesNotMatch(renderer, /addMessage\('正在思考…'/);
  assert.match(renderer, /addThinkingMessage/);
  assert.match(renderer, /thinking-dots/);
  assert.match(preload, /connectorHealth/);
  assert.match(preload, /testWeChatCapture/);
  assert.match(main, /connectors:health/);
  assert.match(main, /wechat:test-capture/);
  assert.match(workbench, /感知采集状态/);
  assert.match(workbench, /测试微信采集/);
  assert.match(html, /panel-health/);
});
