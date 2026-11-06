'use strict';

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const desktopRoot = path.resolve(__dirname, '..');
const repoRoot = path.resolve(desktopRoot, '..');

function read(file) {
  return fs.readFileSync(path.join(desktopRoot, file), 'utf8');
}

function readRepo(file) {
  return fs.readFileSync(path.join(repoRoot, file), 'utf8');
}

test('Desktop brief renders Markdown instead of raw <pre> source', () => {
  const workbench = read('workbench.js');
  const html = read('workbench.html');

  assert.match(html, /<script src="markdown\.js"><\/script>/, 'workbench must load markdown.js');
  assert.match(workbench, /MarkdownLite\.render\(brief\.content_md/, 'brief body must be rendered as Markdown');
  assert.doesNotMatch(workbench, /el\('pre', '', brief\.content_md/, 'brief must not print Markdown source in <pre>');
  // 简报历史与版本导航。
  assert.match(workbench, /listBriefs/, 'workbench must load brief history');
  assert.match(workbench, /briefHistory/, 'workbench must keep brief history in state');
});

test('Workbench task card can jump to the Worker conversation', () => {
  const workbench = read('workbench.js');
  const preload = read('preload.js');
  const main = read('main.js');
  const renderer = read('renderer.js');

  assert.match(workbench, /查看 Worker 对话/, 'workbench must offer a Worker conversation entry');
  assert.match(workbench, /current_session_id/, 'workbench must find the worker session of the task');
  assert.match(preload, /openConversation/, 'preload must expose openConversation');
  assert.match(main, /assistant:open-conversation/, 'main must handle assistant:open-conversation');
  assert.match(main, /chat:open-conversation/, 'main must notify the renderer');
  assert.match(preload, /onOpenConversation/, 'preload must expose onOpenConversation');
  assert.match(renderer, /onOpenConversation/, 'renderer must react to the open-conversation event');
});

test('Desktop history uses the unified conversation projection', () => {
  const renderer = read('renderer.js');
  const preload = read('preload.js');
  const main = read('main.js');
  const index = read('index.html');

  assert.match(index, /history-tab-main/, 'history panel must have a main-conversation tab');
  assert.match(index, /history-tab-worker/, 'history panel must have a worker-conversation tab');
  assert.match(index, /history-tab-delegated/, 'history panel must have a delegated tab');
  assert.match(renderer, /listConversations/, 'renderer must read the unified projection');
  assert.match(preload, /listConversations/, 'preload must expose listConversations');
  assert.match(preload, /conversationMessages/, 'preload must expose conversationMessages');
  assert.match(main, /assistant:conversations/, 'main must handle assistant:conversations');
  assert.match(main, /assistant:conversation-messages/, 'main must handle assistant:conversation-messages');
  // 委派执行记录没有会话时不能伪造成可打开的对话。
  assert.match(renderer, /kind === 'delegated'/, 'renderer must refuse to open delegated records without a session');
});

test('Mobile H5 exposes brief, task center and unified conversations', () => {
  const page = readRepo('web/index.html');

  // 顶部入口：收件箱换成每日简报，通知功能仍在设置里。
  assert.doesNotMatch(page, /title="收件箱"/, 'top bar must not keep the inbox entry');
  assert.match(page, /title="每日简报"/, 'top bar must have the daily brief entry');
  assert.match(page, /title="任务中心"/, 'top bar must have the task center entry');
  assert.match(page, /打开通知收件箱/, 'notification inbox must stay reachable from settings');

  // 简报抽屉：Markdown 渲染 + 状态齐全。
  assert.match(page, /id="brief-panel"/, 'mobile must have a brief drawer');
  assert.match(page, /v-html="md\(briefContent\)"/, 'brief body must be rendered as Markdown');
  assert.doesNotMatch(page, /<pre>\{\{ todayBrief\.content_md \}\}<\/pre>/, 'chat stream must not show the brief as plain text');
  for (const state of ['briefLoading', 'briefError', 'briefOffline', 'briefNotice']) {
    assert.match(page, new RegExp(state), `mobile brief must handle ${state}`);
  }

  // 会话目录三类。
  assert.match(page, /switchConvTab\('main'\)/, 'conversation directory must have a main tab');
  assert.match(page, /switchConvTab\('worker'\)/, 'conversation directory must have a worker tab');
  assert.match(page, /switchConvTab\('delegated'\)/, 'conversation directory must have a delegated tab');

  // Worker 对话页与任务中心。
  assert.match(page, /id="worker-panel"/, 'mobile must have a worker conversation panel');
  assert.match(page, /id="tasks-panel"/, 'mobile must have a task center panel');
  assert.match(page, /taskGroups/, 'mobile task center must group tasks');
  for (const action of ['decideApproval', 'decideArtifact', 'deliverArtifact']) {
    assert.match(page, new RegExp(action), `mobile must support ${action}`);
  }
});

test('The three mobile page copies stay in sync', () => {
  const canonical = readRepo('web/index.html');
  assert.equal(readRepo('desktop/server/web/index.html'), canonical, 'desktop/server/web copy drifted');
  assert.equal(readRepo('android/gosvc/webassets/index.html'), canonical, 'android webassets copy drifted');
});

test('Relay protocol exposes the whitelisted SuperAgent commands', () => {
  const protocol = readRepo('internal/relay/protocol.go');
  const local = readRepo('internal/relay/local_commands.go');
  const phone = readRepo('internal/relay/commands.go');

  // 协议里声明的命令常量，每一条都要在电脑端路由表和手机端构造器里落地。
  const constants = [...protocol.matchAll(/Cmd[A-Za-z]+\s*=\s*"([a-z_]+)"/g)].map(match => match[1]);
  assert.ok(constants.length >= 20, `expected the whitelist to cover the control plane, got ${constants.length}`);
  for (const command of constants) {
    const name = [...protocol.matchAll(/Cmd([A-Za-z]+)\s*=\s*"([a-z_]+)"/g)]
      .find(match => match[2] === command)[1];
    assert.match(local, new RegExp(`Cmd${name}:`), `desktop router missing ${command}`);
    assert.match(phone, new RegExp(name), `phone client missing ${command}`);
  }
  // 结果分类必须区分执行/排队/冲突/拒绝/未知。
  for (const kind of ['CommandExecuted', 'CommandQueued', 'CommandConflict', 'CommandRejected', 'CommandUnknown']) {
    assert.match(protocol, new RegExp(kind), `protocol missing ${kind}`);
  }
  assert.match(phone, /idempotency_key/, 'phone commands must carry an idempotency key');
});

test('Relay protocol keeps the legacy job commands for one compatibility cycle', () => {
  const client = readRepo('internal/relay/client.go');
  assert.match(client, /case "job":/, 'legacy job command must stay');
  assert.match(client, /case "job_status":/, 'legacy job_status command must stay');
  assert.match(client, /case "job_cancel":/, 'legacy job_cancel command must stay');
  // AgentJob 是执行段，返回里要同时带 job_id 与 work_task_id，
  // 手机据此把两种控制面映射起来。
  assert.match(client, /WorkTaskID: job\.Task\.WorkTaskID/, 'job result must carry the work task id');
});

test('Channel switches report where they really run', () => {
  const collection = readRepo('internal/httpapi/collection.go');
  const page = readRepo('web/index.html');

  // 三态：本机可用 / 需电脑在线 / 未接入。
  assert.match(collection, /ChannelAvailabilityLocal\s*=\s*"local"/, 'local availability missing');
  assert.match(collection, /ChannelAvailabilityDesktop\s*=\s*"desktop"/, 'desktop availability missing');
  assert.match(collection, /ChannelAvailabilityUnsupported\s*=\s*"unsupported"/, 'unsupported availability missing');
  assert.match(collection, /wechat_mobile/, 'phone wechat must be listed');
  assert.match(collection, /wechat_desktop/, 'desktop wechat must be listed');
  assert.match(collection, /feishu/, 'unsupported channels must be listed too');

  // 手机界面按三态展示，远程渠道要走桌面真实回执。
  assert.match(page, /collectionChannels/, 'mobile must show the channel availability list');
  assert.match(page, /availabilityLabel/, 'mobile must label the availability');
  assert.match(page, /toggleChannel/, 'mobile must configure channels through the right device');
  assert.match(page, /未接入，暂不可开启/, 'unsupported channels must be disabled on mobile');
});

test('Approvals and acceptance never report a queued offline state as done', () => {
  const remote = readRepo('internal/httpapi/remote.go');
  // 审批、验收、生成简报使用 queueable=false：离线时如实报错而不是排队。
  const highRisk = [...remote.matchAll(/OutboxDecide\([^)]*\)\s*}, (false|true)\)/g)].map(m => m[1]);
  assert.deepEqual(highRisk, ['false'], 'approval decision must not be queued');
  const artifacts = [...remote.matchAll(/ArtifactDecide\([^)]*\)\s*}, (false|true)\)/g)].map(m => m[1]);
  assert.deepEqual(artifacts, ['false'], 'artifact acceptance must not be queued');
  assert.match(remote, /这个操作需要电脑在线才能确认/, 'offline high-risk actions must explain why');
});
