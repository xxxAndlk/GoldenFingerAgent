'use strict';

const api = window.desktopAssistant;
const state = { tab: 'pending', tasks: [], outbox: [], brief: null, briefError: '', channels: null, email: null };
let refreshTimer = null;
let eventRefresh = null;

const statusEl = document.getElementById('status');
const panels = {
  pending: document.getElementById('panel-pending'),
  running: document.getElementById('panel-running'),
  brief: document.getElementById('panel-brief'),
  channels: document.getElementById('panel-channels'),
};

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function today() {
  const now = new Date();
  const pad = value => String(value).padStart(2, '0');
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

function statusClass(status) {
  if (status === 'running' || status === 'queued') return 'running';
  if (status === 'failed' || status === 'interrupted' || status === 'timeout' || status === 'cancelled') return 'failed';
  if (status === 'waiting') return 'pending';
  return '';
}

function card(title, meta, body, actions = []) {
  const root = el('article', 'card');
  root.append(el('h3', '', title));
  if (meta) root.append(el('div', 'meta', meta));
  if (body) root.append(el('div', 'body', body));
  if (actions.length) {
    const row = el('div', 'actions');
    row.append(...actions);
    root.append(row);
  }
  return root;
}

function actionButton(label, onClick, variant = '') {
  const button = el('button', variant, label);
  button.type = 'button';
  button.addEventListener('click', async () => {
    button.disabled = true;
    try {
      await onClick();
    } catch (error) {
      statusEl.textContent = `操作失败：${error.message}`;
    } finally {
      button.disabled = false;
      refresh();
    }
  });
  return button;
}

function workerCard(task) {
  const status = task.waiting_kind && task.waiting_kind !== 'none' ? `等待：${task.waiting_kind}` : task.status;
  const meta = `${status} · 更新 ${new Date(task.updated_at).toLocaleString()} · ${task.id.slice(0, 8)}`;
  const body = task.result_summary || task.output || task.error || task.task || '';
  const actions = [];
  const running = ['queued', 'running'].includes(task.status) || (task.waiting_kind && task.waiting_kind !== 'none');
  if (running) {
    const input = el('input');
    input.type = 'text';
    input.placeholder = '追加指令';
    const steer = actionButton('追加', async () => {
      const text = input.value.trim();
      if (!text) return;
      await api.steerWorkerTask(task.id, text);
      input.value = '';
    }, 'secondary');
    actions.push(input, steer);
    actions.push(actionButton('取消任务', async () => {
      if (!window.confirm('确定取消这个后台任务？')) return;
      await api.cancelWorkerTask(task.id);
    }, 'danger'));
  }
  return card(task.task || '后台任务', meta, body, actions);
}

function outboxCard(item) {
  const meta = `${item.channel} · ${item.kind} · v${item.version} · 待审批`;
  return card('审批：' + (item.kind || item.channel), meta, item.body || '', [
    actionButton('批准', async () => {
      await api.approveOutbox(item.id, item.version);
    }),
    actionButton('驳回', async () => {
      const reason = window.prompt('驳回原因（可留空）', '');
      if (reason === null) return;
      await api.rejectOutbox(item.id, item.version, reason);
    }, 'secondary'),
  ]);
}

function renderPending() {
  const panel = panels.pending;
  panel.replaceChildren();
  const waiting = state.tasks.filter(task =>
    task.status === 'running' && task.waiting_kind && task.waiting_kind !== 'none');
  const failed = state.tasks.filter(task =>
    ['failed', 'interrupted', 'timeout'].includes(task.status));
  if (!state.outbox.length && !waiting.length && !failed.length) {
    panel.append(el('div', 'empty', '没有需要你处理的事项。'));
    return;
  }
  if (state.outbox.length) {
    panel.append(el('h2', '', '待审批'));
    state.outbox.forEach(item => panel.append(outboxCard(item)));
  }
  if (waiting.length) {
    panel.append(el('h2', '', '等待你的澄清 / 审批'));
    waiting.forEach(task => panel.append(workerCard(task)));
  }
  if (failed.length) {
    panel.append(el('h2', '', '异常结束'));
    failed.forEach(task => panel.append(workerCard(task)));
  }
}

function renderRunning() {
  const panel = panels.running;
  panel.replaceChildren();
  const tasks = state.tasks.filter(task => ['queued', 'running'].includes(task.status));
  if (!tasks.length) {
    panel.append(el('div', 'empty', '当前没有运行中的后台任务。'));
    return;
  }
  tasks.forEach(task => panel.append(workerCard(task)));
}

function renderBrief() {
  const panel = panels.brief;
  panel.replaceChildren();
  if (!state.brief) {
    panel.append(el('div', 'empty', state.briefError || '今日简报尚未生成。'));
    return;
  }
  const brief = state.brief;
  const meta = `${brief.brief_date} · 第 ${brief.version} 版 · ${brief.model || '模型'} · ${brief.status}`;
  const root = card('今日简报', meta, '');
  const pre = el('pre', '', brief.content_md || '');
  root.append(pre);
  const actions = el('div', 'actions');
  if (brief.status !== 'delivered') {
    actions.append(actionButton('标记已读', async () => {
      await api.markBriefDelivered(brief.id);
    }, 'secondary'));
  }
  const refs = el('div', 'hint', `来源：${(Array.isArray(brief.source_refs) ? brief.source_refs : []).join('、') || '无'}`);
  root.append(actions, refs);
  panel.append(root);
}

function renderChannels() {
  const panel = panels.channels;
  panel.replaceChildren();
  const data = state.channels;
  const intro = el('div', 'hint',
    '微信默认不采集。只有这里显式允许的来源才会进入原始存储、摘要和每日简报；被拒绝的来源只增加跳过计数，不保存正文。');
  panel.append(intro);

  const form = el('article', 'card');
  form.append(el('h3', '', '新增 / 更新微信来源'));
  const row = el('div', 'row');
  const source = el('input');
  source.type = 'text';
  source.placeholder = '群名或联系人，例如：项目群';
  const select = el('select');
  const allow = el('option', '', '允许采集');
  allow.value = 'allow';
  const deny = el('option', '', '禁止采集');
  deny.value = 'deny';
  select.append(allow, deny);
  const save = actionButton('保存策略', async () => {
    if (!source.value.trim()) throw new Error('请填写来源');
    await api.updateChannelPolicy({
      channel: 'wechat', source: source.value.trim(), allowed: select.value === 'allow',
    });
    source.value = '';
  });
  row.append(source, select, save);
  form.append(row);
  panel.append(form);

  const list = el('article', 'card');
  list.append(el('h3', '', '当前微信策略'));
  const policies = (data && data.policies) || [];
  if (!policies.length) {
    list.append(el('div', 'hint', '还没有配置来源；当前全部拒绝。'));
  } else {
    policies.forEach(item => {
      list.append(el('div', 'body',
        `${item.source || '（默认）'}：${item.allowed ? '允许' : '禁止'} · 更新 ${new Date(item.updated_at).toLocaleString()}`));
    });
  }
  const skips = (data && data.skips_today) || [];
  if (skips.length) {
    list.append(el('h3', '', '今日跳过'));
    skips.forEach(item => list.append(el('div', 'body', `${item.channel}：${item.count} 条`)));
  }
  panel.append(list);
  panel.append(emailSettingsCard());
}

function emailSettingsCard() {
  const value = state.email || {};
  const root = el('article', 'card');
  root.append(el('h3', '', '邮箱（IMAP 只读）'));
  root.append(el('div', 'hint',
    '只读取邮件正文并进入邮件 Thread；不修改已读状态、不发送邮件。密码只保存在本机 settings.json，不会回显。'));

  const makeInput = (type, text, placeholder = '') => {
    const input = el('input');
    input.type = type;
    if (text !== undefined && text !== null) input.value = String(text);
    if (placeholder) input.placeholder = placeholder;
    return input;
  };
  const field = (label, input) => {
    const row = el('div', 'row');
    row.append(el('span', 'hint', label), input);
    return row;
  };

  const enabled = el('input');
  enabled.type = 'checkbox';
  enabled.checked = Boolean(value.enabled);
  const tls = el('input');
  tls.type = 'checkbox';
  tls.checked = value.tls !== false;
  const host = makeInput('text', value.host || '', 'imap.example.com');
  const port = makeInput('number', value.port || 993, '993');
  const username = makeInput('text', value.username || '', '邮箱账号');
  const password = makeInput('password', '', value.has_password ? '已保存，留空保留' : '应用专用密码');
  const mailbox = makeInput('text', value.mailbox || 'INBOX', 'INBOX');
  const initial = makeInput('number', value.initial_fetch || 20, '20');
  const poll = makeInput('number', value.poll_seconds || 300, '300');

  const enableRow = el('div', 'row');
  enableRow.append(el('span', 'hint', '启用 IMAP 采集'), enabled);
  const tlsRow = el('div', 'row');
  tlsRow.append(el('span', 'hint', '使用 TLS'), tls);

  const save = actionButton('保存邮箱配置', async () => {
    await api.saveEmailSettings({
      enabled: enabled.checked,
      host: host.value.trim(),
      port: Number(port.value) || 993,
      username: username.value.trim(),
      password: password.value,
      mailbox: mailbox.value.trim() || 'INBOX',
      tls: tls.checked,
      initial_fetch: Number(initial.value) || 20,
      poll_seconds: Number(poll.value) || 300,
    });
  });

  root.append(
    enableRow,
    field('IMAP 主机', host),
    field('端口', port),
    field('账号', username),
    field('密码', password),
    field('邮箱文件夹', mailbox),
    tlsRow,
    field('首次拉取条数', initial),
    field('轮询间隔（秒）', poll),
  );
  const actions = el('div', 'actions');
  actions.append(save);
  root.append(actions);
  return root;
}

function render() {
  document.querySelectorAll('nav button').forEach(button => {
    button.classList.toggle('active', button.dataset.tab === state.tab);
  });
  Object.entries(panels).forEach(([name, panel]) => { panel.hidden = name !== state.tab; });
  if (state.tab === 'pending') renderPending();
  else if (state.tab === 'running') renderRunning();
  else if (state.tab === 'brief') renderBrief();
  else if (state.tab === 'channels') renderChannels();
}

async function refresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(async () => {
    const results = await Promise.allSettled([
      api.listWorkerTasks(''),
      api.listOutbox('pending_approval'),
      api.latestBrief(today()),
      api.channelSettings(),
      api.getEmailSettings(),
    ]);
    if (results[0].status === 'fulfilled') state.tasks = results[0].value.tasks || [];
    if (results[1].status === 'fulfilled') state.outbox = results[1].value.outbox || [];
    if (results[2].status === 'fulfilled') {
      state.brief = results[2].value.brief || null;
      state.briefError = '';
    } else {
      state.brief = null;
      state.briefError = results[2].reason?.message || '今日简报尚未生成。';
    }
    if (results[3].status === 'fulfilled') state.channels = results[3].value;
    if (results[4].status === 'fulfilled') state.email = results[4].value;
    statusEl.textContent = results.some(result => result.status === 'rejected') ? '部分数据读取失败' : '已连接';
    render();
  }, 50);
}

document.addEventListener('click', event => {
  const button = event.target.closest('nav button');
  if (!button) return;
  state.tab = button.dataset.tab;
  render();
  refresh();
});
document.getElementById('refresh').addEventListener('click', refresh);
document.getElementById('generate').addEventListener('click', async event => {
  const button = event.currentTarget;
  button.disabled = true;
  statusEl.textContent = '正在生成今日简报…';
  try {
    await api.generateBrief(today());
    await refresh();
  } catch (error) {
    statusEl.textContent = `生成失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
});

if (api.onAgentEvent) {
  api.onAgentEvent(event => {
    const kind = String(event && event.kind || '');
    if (!kind) return;
    clearTimeout(eventRefresh);
    eventRefresh = setTimeout(refresh, 500);
  });
}

api.listWorkerTasks('')
  .then(() => { statusEl.textContent = '已连接'; })
  .catch(error => { statusEl.textContent = `未连接：${error.message}`; });
refresh();
setInterval(refresh, 20000);
