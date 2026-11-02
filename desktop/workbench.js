'use strict';

const api = window.desktopAssistant;
const state = {
  tab: 'pending', tasks: [], outbox: [], brief: null, briefError: '',
  channels: null, email: null, webSites: [], webStatus: {}, webError: '',
};
let refreshTimer = null;
let eventRefresh = null;

const statusEl = document.getElementById('status');
const panels = {
  pending: document.getElementById('panel-pending'),
  running: document.getElementById('panel-running'),
  brief: document.getElementById('panel-brief'),
  channels: document.getElementById('panel-channels'),
  web: document.getElementById('panel-web'),
};

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function splitList(value) {
  return String(value || '')
    .split(/[,，\n]/)
    .map(item => item.trim())
    .filter(Boolean);
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

const CHANNEL_LABELS = {
  activity: '工作轨迹',
  email: '邮箱',
  feishu: '飞书',
  dingtalk: '钉钉',
  wechat_desktop: '微信（电脑）',
  wechat_mobile: '微信（手机）',
};

const CHANNEL_FLAGS = [
  ['enabled', '采集新信息'],
  ['summary_enabled', '生成摘要'],
  ['index_enabled', '生成索引'],
  ['brief_enabled', '纳入每日简报'],
  ['auto_worker_enabled', '允许自动 Worker'],
];

function checkboxField(label, checked, description = '') {
  const row = el('label', 'row');
  const input = el('input');
  input.type = 'checkbox';
  input.checked = Boolean(checked);
  row.append(input, el('span', checked ? '' : 'hint', label));
  if (description) row.append(el('span', 'hint', description));
  return { input, row };
}

function renderChannels() {
  const panel = panels.channels;
  panel.replaceChildren();
  const data = state.channels;
  panel.append(el('div', 'hint',
    '六类渠道默认全部关闭。关闭采集时不会入库；关闭摘要、索引或简报后，对应任务确实不再执行。被拒绝来源只增加跳过计数，不保存正文。'));

  const globalCard = el('article', 'card');
  globalCard.append(el('h3', '', '全局能力'));
  const global = (data && data.global) || {};
  const globalFlags = [
    ['router_enabled', 'Router 自动路由'],
    ['worker_enabled', '允许自动 Worker'],
    ['daily_brief_enabled', '每日简报自动生成'],
    ['proactive_notify_enabled', '主动通知'],
  ];
  const globalInputs = {};
  globalFlags.forEach(([key, label]) => {
    const field = checkboxField(label, global[key] !== false);
    globalInputs[key] = field.input;
    globalCard.append(field.row);
  });
  globalCard.append(el('div', 'hint', '渠道自动 Worker 需要渠道和全局同时允许；手动生成简报不受每日自动开关影响。'));
  const globalActions = el('div', 'actions');
  globalActions.append(actionButton('保存全局开关', async () => {
    const payload = {};
    Object.entries(globalInputs).forEach(([key, input]) => { payload[key] = input.checked; });
    await api.updateGlobalConfig(payload);
    await api.refreshChannelCache?.();
  }));
  globalCard.append(globalActions);
  panel.append(globalCard);

  const configs = (data && data.configs) || [];
  configs.forEach(config => {
    const card = el('article', 'card');
    card.append(el('h3', '', CHANNEL_LABELS[config.channel] || config.channel));
    const inputs = {};
    CHANNEL_FLAGS.forEach(([key, label]) => {
      const field = checkboxField(label, config[key]);
      inputs[key] = field.input;
      card.append(field.row);
    });
    const actions = el('div', 'actions');
    actions.append(actionButton('保存渠道开关', async () => {
      const payload = {};
      Object.entries(inputs).forEach(([key, input]) => { payload[key] = input.checked; });
      await api.updateChannelConfig(config.channel, payload);
      await api.refreshChannelCache?.();
    }));
    card.append(actions);
    panel.append(card);
  });

  const form = el('article', 'card');
  form.append(el('h3', '', '来源允许 / 拒绝'));
  form.append(el('div', 'hint',
    '新群、新联系人默认拒绝。来源填写群名、联系人、邮箱账号或应用名；只对所选渠道生效。'));
  const row = el('div', 'row');
  const channel = el('select');
  (data && data.channels || Object.keys(CHANNEL_LABELS)).forEach(name => {
    const option = el('option', '', CHANNEL_LABELS[name] || name);
    option.value = name;
    channel.append(option);
  });
  channel.value = 'wechat_desktop';
  const source = el('input');
  source.type = 'text';
  source.placeholder = '群名 / 联系人 / 账号';
  const select = el('select');
  const allow = el('option', '', '允许采集');
  allow.value = 'allow';
  const deny = el('option', '', '禁止采集');
  deny.value = 'deny';
  select.append(allow, deny);
  const save = actionButton('保存来源策略', async () => {
    if (!source.value.trim()) throw new Error('请填写来源');
    await api.updateChannelPolicy({
      channel: channel.value, source: source.value.trim(), allowed: select.value === 'allow',
    });
    await api.refreshChannelCache?.();
    source.value = '';
  });
  row.append(channel, source, select, save);
  form.append(row);
  panel.append(form);

  const list = el('article', 'card');
  list.append(el('h3', '', '当前来源策略'));
  const policies = (data && data.policies) || [];
  if (!policies.length) {
    list.append(el('div', 'hint', '还没有配置来源；微信、飞书和钉钉全部拒绝，其他渠道按默认策略处理。'));
  } else {
    policies.forEach(item => {
      list.append(el('div', 'body',
        `${CHANNEL_LABELS[item.channel] || item.channel} · ${item.source || '（默认）'}：${item.allowed ? '允许' : '禁止'} · 更新 ${new Date(item.updated_at).toLocaleString()}`));
    });
  }
  const skips = (data && data.skips_today) || [];
  if (skips.length) {
    list.append(el('h3', '', '今日跳过'));
    skips.forEach(item => list.append(el('div', 'body',
      `${CHANNEL_LABELS[item.channel] || item.channel}：${item.count} 条`)));
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
  const mailboxes = makeInput('text', (value.mailboxes || []).join(', '), 'INBOX, 项目/客户');
  const senders = makeInput('text', (value.senders || []).join(', '), 'client@example.com, boss@…');
  const subjects = makeInput('text', (value.subject_filters || []).join(', '), '报价, 合同');
  const sinceDays = makeInput('number', value.since_days || 0, '0 表示不限制');
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
      mailboxes: splitList(mailboxes.value),
      senders: splitList(senders.value),
      subject_filters: splitList(subjects.value),
      since_days: Number(sinceDays.value) || 0,
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
    field('默认文件夹', mailbox),
    field('其他文件夹（逗号分隔）', mailboxes),
    field('发件人筛选（逗号分隔）', senders),
    field('主题筛选（逗号分隔）', subjects),
    field('只采集最近天数（0 不限）', sinceDays),
    tlsRow,
    field('首次拉取条数', initial),
    field('轮询间隔（秒）', poll),
  );
  const actions = el('div', 'actions');
  actions.append(save);
  root.append(actions);
  return root;
}

function webSiteCard(site, isNew = false) {
  const card = el('article', 'card');
  card.append(el('h3', '', isNew ? '新增网页站点' : (site.id || '网页站点')));
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
  const id = makeInput('text', site.id || '', 'site-id（字母/数字/-_）');
  if (!isNew) id.readOnly = true;
  const account = makeInput('text', site.account || '', '账号别名');
  const startUrls = makeInput('text', (site.start_urls || []).join(', '), 'https://example.com/authorized/path');
  const origins = makeInput('text', (site.allowed_origins || []).join(', '), '留空则从起始 URL 推导');
  const prefixes = makeInput('text', (site.allowed_path_prefixes || []).join(', '), '/authorized/');
  const interval = makeInput('number', site.interval_seconds || 1800, '1800');
  const maxPages = makeInput('number', site.max_pages || 5, '5');
  const maxDepth = makeInput('number', site.max_depth || 0, '0');
  const loginUrl = makeInput('text', site.login_url || '', 'https://example.com/login');
  const credentialRef = makeInput('text', site.credential_ref || '', 'credential-ref');
  const usernameSelector = makeInput('text', site.username_selector || '', '#username');
  const passwordSelector = makeInput('text', site.password_selector || '', '#password');
  const submitSelector = makeInput('text', site.submit_selector || '', 'button[type=submit]');
  const credentialUser = makeInput('text', '', '登录账号（仅本次输入）');
  const credentialPassword = makeInput('password', '', '登录密码（仅本次输入）');

  const toggles = {};
  const toggleDefs = [
    ['enabled', '采集新信息'],
    ['navigation_enabled', '允许在授权范围分页/滚动'],
    ['session_enabled', '会话已验证（允许无头采集）'],
    ['summary_enabled', '生成摘要'],
    ['index_enabled', '生成索引'],
    ['brief_enabled', '纳入每日简报'],
    ['auto_worker_enabled', '允许自动 Worker'],
    ['auto_login_enabled', '允许自动登录'],
  ];
  toggleDefs.forEach(([key, label]) => {
    const fieldRow = checkboxField(label, site[key]);
    toggles[key] = fieldRow.input;
    card.append(fieldRow.row);
  });
  card.append(
    field('站点 ID', id),
    field('账号别名', account),
    field('起始 URL', startUrls),
    field('授权 origin', origins),
    field('授权路径前缀', prefixes),
    field('采集间隔（秒）', interval),
    field('最大页数', maxPages),
    field('最大深度', maxDepth),
    field('登录 URL', loginUrl),
    field('凭据引用', credentialRef),
    field('账号选择器', usernameSelector),
    field('密码选择器', passwordSelector),
    field('提交选择器', submitSelector),
    field('账号（仅加密保存）', credentialUser),
    field('密码（仅加密保存）', credentialPassword),
  );
  card.append(el('div', 'hint',
    '自动登录先保存以上配置，再保存账号密码。密码只进入 Electron safeStorage（Windows DPAPI）加密存储，不写 settings.json、数据库、日志或模型上下文；每次填充前需要确认。验证码、扫码和二次验证只接受人工接续。'));

  const status = state.webStatus[site.id] || {};
  if (!isNew) {
    const statusText = status.running
      ? '正在采集…'
      : `${status.lastRunAt ? `上次 ${new Date(status.lastRunAt).toLocaleString()}` : '尚未运行'} · 本轮 ${status.items || 0} 条${status.loginRequired ? ' · 需要人工登录' : ''}${status.lastError ? ` · ${status.lastError}` : ''}`;
    card.append(el('div', 'meta', statusText));
  }
  const actions = el('div', 'actions');
  actions.append(actionButton(isNew ? '新增站点' : '保存站点', async () => {
    const payload = {
      id: id.value.trim(),
      account: account.value.trim(),
      start_urls: splitList(startUrls.value),
      allowed_origins: splitList(origins.value),
      allowed_path_prefixes: splitList(prefixes.value),
      interval_seconds: Number(interval.value) || 0,
      max_pages: Number(maxPages.value) || 0,
      max_depth: Number(maxDepth.value) || 0,
      login_url: loginUrl.value.trim(),
      credential_ref: credentialRef.value.trim(),
      username_selector: usernameSelector.value.trim(),
      password_selector: passwordSelector.value.trim(),
      submit_selector: submitSelector.value.trim(),
    };
    Object.entries(toggles).forEach(([key, input]) => { payload[key] = input.checked; });
    await api.saveWebSite(payload);
  }));
  if (!isNew) {
    actions.append(actionButton('立即采集', async () => {
      await api.runBrowserSite(site.id);
    }, 'secondary'));
    actions.append(actionButton('人工 / 自动登录', async () => {
      await api.loginBrowserSite(site.id, (site.start_urls || [])[0] || '');
    }, 'secondary'));
    actions.append(actionButton('保存凭据', async () => {
      const ref = credentialRef.value.trim();
      if (!ref) throw new Error('请先填写凭据引用并保存站点');
      if (!credentialUser.value || !credentialPassword.value) throw new Error('请填写账号和密码');
      await api.saveBrowserCredential(ref, {
        username: credentialUser.value,
        password: credentialPassword.value,
      });
      credentialUser.value = '';
      credentialPassword.value = '';
      statusEl.textContent = '凭据已加密保存';
    }, 'secondary'));
    actions.append(actionButton('删除凭据', async () => {
      const ref = credentialRef.value.trim();
      if (!ref) return;
      if (!window.confirm(`删除凭据 ${ref}？`)) return;
      await api.deleteBrowserCredential(ref);
      statusEl.textContent = '凭据已删除';
    }, 'secondary'));
    actions.append(actionButton('停止', async () => {
      await api.stopBrowserSite(site.id);
    }, 'secondary'));
    actions.append(actionButton('读取当前页', async () => {
      const url = window.prompt('输入该站点授权范围内的页面 URL', (site.start_urls || [])[0] || '');
      if (!url) return;
      const result = await api.readCurrentPage(site.id, url);
      statusEl.textContent = `已读取 ${result.extractedBy}（${result.completeness}）：${String(result.text || '').slice(0, 80)}`;
    }, 'secondary'));
    actions.append(actionButton('删除站点', async () => {
      if (!window.confirm(`确定删除站点 ${site.id}？已采集记录不会删除。`)) return;
      await api.deleteWebSite(site.id);
    }, 'danger'));
  }
  card.append(actions);
  return card;
}

function renderWeb() {
  const panel = panels.web;
  panel.replaceChildren();
  panel.append(el('div', 'hint',
    '网页采集只读取授权 origin 和路径内的页面。未启用的站点不会启动浏览器；网页内容始终是不可信数据，不能扩大工具权限。'));
  if (state.webError) panel.append(el('div', 'hint', state.webError));
  if (!state.webSites.length) {
    panel.append(el('div', 'empty', '还没有配置网页站点。'));
  } else {
    state.webSites.forEach(site => panel.append(webSiteCard(site)));
  }
  panel.append(webSiteCard({ enabled: false, interval_seconds: 1800, max_pages: 5, max_depth: 0 }, true));
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
  else if (state.tab === 'web') renderWeb();
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
      api.listWebSites(),
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
    if (results[5].status === 'fulfilled') {
      state.webSites = results[5].value.sites || [];
      state.webError = '';
      const statuses = await Promise.allSettled(
        state.webSites.map(site => api.browserStatus(site.id)));
      state.webStatus = {};
      statuses.forEach((result, index) => {
        if (result.status === 'fulfilled') state.webStatus[state.webSites[index].id] = result.value;
      });
    } else {
      state.webSites = [];
      state.webError = results[5].reason?.message || '网页站点读取失败';
    }
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
