'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function read(name) {
  return fs.readFileSync(path.join(__dirname, '..', name), 'utf8');
}

test('Worker 会话的输入走任务 Inbox，不落到普通聊天', () => {
  const renderer = read('renderer.js');
  // 打开 Worker 会话时不调用 useSession：主会话指针不能被挪走。
  assert.match(renderer, /if \(kind === 'main'\) \{\s*await api\.useSession\(id\);/);
  assert.match(renderer, /async function sendWorkerInstruction\(/);
  // 追加指令必须调用 messageWorkTask，而不是 api.chat。
  assert.match(renderer, /api\.messageWorkTask\(taskID, text, task\.version\)/);
  // 发送前读一次任务：确认没到终态，并用最新版本做 CAS。
  assert.match(renderer, /const detail = await api\.getWorkTask\(taskID\);/);
  assert.match(renderer, /isTerminalTaskStatus\(task\.status\)/);
  // 发送分支按当前查看模式走，不能一律 api.chat。
  assert.match(renderer, /if \(workerMode\) \{\s*await sendWorkerInstruction\(text, files\);/);
});

test('主进程打开会话不再改动主聊天会话指针', () => {
  const main = read('main.js');
  const match = main.match(/ipcMain\.handle\('assistant:open-conversation'[\s\S]*?\n\}\);/);
  assert.ok(match, 'open-conversation handler missing');
  // 旧实现会把 chatSessionId 改成被打开的会话，
  // 导致 Worker 会话污染主对话；这里必须保持主会话指针不变。
  assert.doesNotMatch(match[0], /chatSessionId\s*=/);
  assert.match(match[0], /\.\.\.\(meta && typeof meta === 'object'/);
});

test('工作台打开 Worker 对话时带上 kind/task_id', () => {
  const workbench = read('workbench.js');
  assert.match(workbench, /kind: 'worker'/);
  assert.match(workbench, /task_id: task\.id/);
  assert.match(workbench, /can_message: task\.status !== 'completed'/);
});

test('终态任务的输入被明确拦住', () => {
  const renderer = read('renderer.js');
  assert.match(renderer, /function isTerminalTaskStatus\(status\)/);
  assert.match(renderer, /任务已结束，先重新打开/);
  assert.match(renderer, /input\.disabled = Boolean\(blocked\)/);
});

test('委派执行记录在手机和桌面都只读', () => {
  const web = fs.readFileSync(path.join(__dirname, '..', '..', 'web', 'index.html'), 'utf8');
  // 打开委派记录时显式传 can_message:false，不能沿用目录默认值。
  assert.match(web, /openDelegatedConversation\(item\) \{[\s\S]*?can_message: false/);
  // 输入区同时要求可写和任务 ID：没有 Inbox 通道就不渲染输入框。
  assert.match(web, /v-if="workerCanMessage && workerTaskId"/);
  assert.match(web, /if \(!text \|\| !this\.workerTaskId\) return;/);

  const renderer = read('renderer.js');
  assert.match(renderer, /kind === 'delegated'[\s\S]*?can_message: false/);
  // 桌面提交入口直接拦掉委派模式，避免静默无响应。
  assert.match(renderer, /conversationKind === 'delegated'\) return;/);
  assert.match(renderer, /委派执行记录只读/);
});
