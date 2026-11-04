'use strict';

const api = window.desktopAssistant;
const state = {
  tab: 'tasks', workTasks: [], taskDetail: null, taskEvents: [], busyTask: '',
  outbox: [], brief: null, briefError: '',
  channels: null, email: null, webSites: [], webStatus: {}, webError: '',
  health: null, wechatTest: null,
};
let refreshTimer = null;
let eventRefresh = null;

const statusEl = document.getElementById('status');
const panels = {
  tasks: document.getElementById('panel-tasks'),
  pending: document.getElementById('panel-pending'),
  brief: document.getElementById('panel-brief'),
  access: document.getElementById('panel-access'),
};
const channelPanel = document.getElementById('panel-channels');
const webPanel = document.getElementById('panel-web');
const healthPanel = document.getElementById('panel-health');
const pendingCountEl = document.getElementById('pending-count');

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
  if (['queued', 'active', 'running'].includes(status)) return 'running';
  if (['failed', 'interrupted', 'timeout', 'cancelled'].includes(status)) return 'failed';
  if (status && String(status).startsWith('waiting') || status === 'pending_acceptance') return 'pending';
  return '';
}

const TASK_STATUS_LABELS = {
  draft: '草稿', queued: '排队中', active: '执行中', waiting_user: '等待你补充',
  waiting_approval: '等待审批', waiting_other: '等待其他条件', waiting_device: '等待设备',
  waiting_external: '等待外部', paused: '已暂停', pending_acceptance: '待验收',
  completed: '已完成', failed: '异常', cancelled: '已取消',
};

function taskStatus(task) {
  return TASK_STATUS_LABELS[task.status] || task.status;
}

function waitingText(detail) {
  if (!detail || !detail.live_state) return '';
  const live = detail.live_state;
  if (live.waiting_kind && live.waiting_kind !== 'none') return `等待：${live.waiting_kind}`;
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

function statusBadge(task) {
  return el('span', `badge ${statusClass(task.status)}`, taskStatus(task));
}

function taskCard(task, detail) {
  const shownDetail = detail ||
    (state.taskDetail && state.taskDetail.task.id === task.id ? state.taskDetail : null);
  const root = el('article', 'card task-card');
  const head = el('div', 'task-head');
  head.append(el('h3', '', task.title || '长期任务'), statusBadge(task));
  root.append(head);
  root.append(el('div', 'meta', `更新 ${new Date(task.updated_at).toLocaleString()} · ${task.profile || 'read'}`));

  const lines = [];
  if (task.goal) lines.push(task.goal);
  if (shownDetail && shownDetail.live_state) {
    const live = shownDetail.live_state;
    if (live.next_action) lines.push(`下一步：${live.next_action}`);
    else if (live.summary) lines.push(`进展：${live.summary}`);
    const wait = waitingText(shownDetail);
    if (wait) lines.push(wait);
  }
  if (task.completion_criteria) lines.push(`完成标准：${task.completion_criteria}`);
  if (lines.length) root.append(el('div', 'body', lines.join('\n')));

  const expanded = Boolean(shownDetail);
  const actions = el('div', 'actions');
  if (task.status === 'pending_acceptance') {
    actions.append(actionButton('验收完成', async () => {
      await api.completeWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }));
  } else if (task.status === 'failed') {
    actions.append(actionButton('恢复', async () => {
      await api.recoverWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }));
  } else if (task.status === 'paused') {
    actions.append(actionButton('继续', async () => {
      await api.resumeWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }));
  } else if (String(task.status).startsWith('waiting')) {
    actions.append(actionButton('去处理', async () => {
      if (!expanded) await loadTaskDetail(task.id, true);
    }));
  }
  actions.append(actionButton(expanded ? '收起' : '查看详情', async () => {
    if (expanded) {
      state.taskDetail = null;
      state.taskEvents = [];
      render();
    } else {
      await loadTaskDetail(task.id, true);
    }
  }, actions.childElementCount ? 'secondary' : ''));

  root.append(actions);
  if (expanded) root.append(renderTaskDetail(shownDetail, state.taskEvents));
  return root;
}

function artifactCard(artifact, latest) {
  const root = el('article', 'card subcard');
  root.append(el('h3', '', artifact.title || '交付物'));
  root.append(el('div', 'meta', `${artifact.kind} · ${artifact.status} · v${artifact.current_version}`));
  const content = latest && latest.content_text || '';
  if (content) root.append(el('div', 'body', content.slice(0, 1200)));
  const actions = el('div', 'actions');
  if (artifact.status === 'draft') {
    actions.append(actionButton('提交验收', async () => {
      await api.submitArtifact(artifact.id, artifact.current_version);
      await loadTaskDetail(artifact.task_id, false);
    }));
  }
  if (artifact.status === 'pending_acceptance') {
    actions.append(actionButton('验收', async () => {
      await api.acceptArtifact(artifact.id, artifact.current_version);
      await loadTaskDetail(artifact.task_id, false);
    }));
    actions.append(actionButton('要求修改', async () => {
      const reason = window.prompt('请填写修改意见', '');
      if (reason === null) return;
      await api.rejectArtifact(artifact.id, artifact.current_version, reason);
      await loadTaskDetail(artifact.task_id, false);
    }, 'secondary'));
    if (artifact.current_version > 1) {
      actions.append(actionButton('版本差异', async () => {
        const result = await api.diffArtifact(artifact.id, artifact.current_version - 1, artifact.current_version);
        window.alert(result.diff || '没有文本差异');
      }, 'secondary'));
    }
  }
  root.append(actions);
  return root;
}

function grantCard(grant, taskId) {
  const root = el('article', 'card subcard');
  root.append(el('h3', '', grant.action_type));
  root.append(el('div', 'meta',
    `${grant.risk_level} · ${grant.status} · 剩余 ${grant.remaining_uses < 0 ? '不限' : grant.remaining_uses} · 到期 ${grant.expires_at ? new Date(grant.expires_at).toLocaleString() : '不限'}`));
  const actions = el('div', 'actions');
  if (grant.status === 'active') {
    actions.append(actionButton('撤销授权', async () => {
      await api.revokeGrant(grant.id);
      await loadTaskDetail(taskId, false);
    }, 'danger'));
  }
  root.append(actions);
  return root;
}

function renderTaskDetail(detail, events) {
  const wrap = el('div', 'task-detail');
  const task = detail.task;
  const terminal = ['completed', 'cancelled'].includes(task.status);

  wrap.append(el('h2', '', '最近进展'));
  if (detail.live_state) {
    const live = detail.live_state;
    const lines = [];
    if (live.stage) lines.push(`阶段：${live.stage}`);
    if (live.next_action) lines.push(`下一步：${live.next_action}`);
    if (live.waiting_kind && live.waiting_kind !== 'none') lines.push(`等待：${live.waiting_kind}`);
    if (live.handoff_note) lines.push(`交接：${live.handoff_note}`);
    if (lines.length) wrap.append(el('div', 'body', lines.join('\n')));
  }

  if (!terminal) {
    wrap.append(el('h2', '', '追加指令'));
    const row = el('div', 'row');
    const input = el('input');
    input.type = 'text';
    input.placeholder = '补充要求，或告诉管家下一步怎么做';
    input.className = 'task-input';
    const send = actionButton('发送', async () => {
      const text = input.value.trim();
      if (!text) throw new Error('请先输入要补充的内容');
      const result = await api.messageWorkTask(task.id, text, task.version);
      input.value = '';
      statusEl.textContent = result && result.job
        ? '已收到，新执行段已排队。'
        : '已收到，当前执行段到安全位置后会处理。';
      await loadTaskDetail(task.id, false);
    });
    row.append(input, send);
    wrap.append(row);
  }

  const controls = el('div', 'detail-controls');
  if (['queued', 'active', 'waiting_user', 'waiting_approval', 'waiting_other', 'waiting_device', 'waiting_external'].includes(task.status)) {
    controls.append(actionButton('暂停', async () => {
      await api.pauseWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }, 'secondary'));
  }
  if (task.status === 'paused') {
    controls.append(actionButton('继续', async () => {
      await api.resumeWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }));
  }
  if (task.status === 'failed') {
    controls.append(actionButton('恢复', async () => {
      await api.recoverWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }));
  }
  if (task.status === 'pending_acceptance') {
    controls.append(actionButton('验收完成', async () => {
      await api.completeWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }));
  }
  if (task.status === 'completed') {
    controls.append(actionButton('重新打开', async () => {
      await api.reopenWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }, 'secondary'));
  }
  if (!terminal) {
    controls.append(actionButton('取消任务', async () => {
      if (!window.confirm('确定取消这个长期任务？')) return;
      await api.cancelWorkTask(task.id, task.version);
      await loadTaskDetail(task.id, false);
    }, 'danger'));
  }
  if (controls.childElementCount) wrap.append(controls);

  wrap.append(el('h2', '', '最近事件'));
  if (!events || !events.length) {
    wrap.append(el('div', 'empty', '暂无事件。'));
  } else {
    const list = el('div', 'timeline');
    events.slice(-12).reverse().forEach(event => {
      const row = el('div', 'timeline-row');
      row.append(el('span', 'meta', `${new Date(event.created_at).toLocaleString()} · ${event.kind}`));
      row.append(el('div', 'body', String(event.payload_json || event.payload || '')));
      list.append(row);
    });
    wrap.append(list);
  }

  const advanced = el('details', 'fold');
  advanced.append(el('summary', '', '交付物与授权（高级）'));
  const advancedBody = el('div');

  advancedBody.append(el('h2', '', '新建交付物草稿'));
  const addArtifact = el('article', 'card subcard');
  const title = el('input');
  title.type = 'text';
  title.placeholder = '标题';
  const content = el('textarea');
  content.rows = 4;
  content.placeholder = '交付内容';
  const create = actionButton('创建草稿', async () => {
    if (!content.value.trim()) throw new Error('内容不能为空');
    await api.createArtifact(task.id, { kind: 'text', title: title.value.trim(), content: content.value });
    title.value = '';
    content.value = '';
    await loadTaskDetail(task.id, false);
  });
  addArtifact.append(title, content, el('div', 'actions'));
  addArtifact.lastChild.append(create);
  advancedBody.append(addArtifact);

  advancedBody.append(el('h2', '', '交付物'));
  if (!detail.artifacts || !detail.artifacts.length) {
    advancedBody.append(el('div', 'empty', '暂无交付物；管家产出后会进入验收。'));
  } else {
    detail.artifacts.forEach(item => advancedBody.append(
      artifactCard(item, detail.artifact_latest && detail.artifact_latest[item.id])));
  }

  advancedBody.append(el('h2', '', '授权'));
  const grantForm = el('article', 'card subcard');
  grantForm.append(el('h3', '', '新建一次性授权'));
  const tool = el('input');
  tool.type = 'text';
  tool.placeholder = '工具名，如 bash / delegate_agent';
  const risk = el('select');
  ['prepare', 'external_write', 'sensitive'].forEach(value => {
    const option = el('option', '', value);
    option.value = value;
    risk.append(option);
  });
  const uses = el('input');
  uses.type = 'number';
  uses.value = '1';
  uses.placeholder = '次数';
  const expires = el('input');
  expires.type = 'number';
  expires.value = '3600';
  expires.placeholder = '有效秒数';
  const saveGrant = actionButton('创建授权', async () => {
    if (!tool.value.trim()) throw new Error('请填写工具名');
    await api.createTaskGrant(task.id, {
      action_type: tool.value.trim(), risk_level: risk.value,
      remaining_uses: Number(uses.value) || 1, expires_in_seconds: Number(expires.value) || 3600,
    });
    tool.value = '';
    await loadTaskDetail(task.id, false);
  });
  grantForm.append(tool, risk, uses, expires, el('div', 'actions'));
  grantForm.lastChild.append(saveGrant);
  advancedBody.append(grantForm);
  if (!detail.grants || !detail.grants.length) {
    advancedBody.append(el('div', 'empty', '暂无授权。'));
  } else {
    detail.grants.forEach(grant => advancedBody.append(grantCard(grant, task.id)));
  }

  if (detail.jobs && detail.jobs.length) {
    advancedBody.append(el('h2', '', '执行段'));
    detail.jobs.slice(0, 8).forEach(job => {
      advancedBody.append(el('div', 'body',
        `${job.status} · ${job.id.slice(0, 8)} · ${job.result_summary || job.error || job.task}`));
    });
  }

  advanced.append(advancedBody);
  wrap.append(advanced);
  return wrap;
}

async function loadTaskDetail(taskId, force) {
  if (!taskId) return;
  if (!force && state.taskDetail && state.taskDetail.task.id === taskId) {
    // 保持展开状态刷新
  }
  state.busyTask = taskId;
  try {
    const [detail, eventPage] = await Promise.all([
      api.getWorkTask(taskId),
      api.listTaskEvents(taskId, 0),
    ]);
    state.taskDetail = detail;
    state.taskEvents = eventPage.events || [];
  } finally {
    state.busyTask = '';
    render();
  }
}

function renderPending() {
  const panel = panels.pending;
  panel.replaceChildren();
  panel.append(el('div', 'hint', '这里只放需要你动手的事：批准草稿、补充信息、验收结果或处理异常。处理完会自动消失。'));

  const waiting = state.workTasks.filter(task =>
    String(task.status).startsWith('waiting') || task.status === 'pending_acceptance');
  const failed = state.workTasks.filter(task => task.status === 'failed');
  if (!state.outbox.length && !waiting.length && !failed.length) {
    panel.append(el('div', 'empty', '现在没有需要你处理的事项。'));
    return;
  }
  if (state.outbox.length) {
    panel.append(el('h2', '', `待审批 · ${state.outbox.length}`));
    state.outbox.forEach(item => panel.append(outboxCard(item)));
  }
  if (waiting.length) {
    panel.append(el('h2', '', `等待你的补充 / 验收 · ${waiting.length}`));
    waiting.forEach(task => panel.append(taskCard(task)));
  }
  if (failed.length) {
    panel.append(el('h2', '', `异常任务 · ${failed.length}`));
    failed.forEach(task => panel.append(taskCard(task)));
  }
}

function renderTasks() {
  const panel = panels.tasks;
  panel.replaceChildren();

  const intro = el('article', 'card');
  intro.append(el('h3', '', '工作台怎么用'));
  const steps = el('ol', 'steps');
  [
    ['告诉管家要做什么', '在下面写清楚目标，点「开始执行」。'],
    ['忙别的就行', '它会在后台分段做；缺信息、要审批或要验收时，顶部「待处理」会提醒你。'],
    ['确认后才算完成', '在「待处理」或任务详情里验收；不满意就让它继续改。'],
  ].forEach(([title, text]) => {
    const item = el('li');
    item.append(el('strong', '', title), el('span', '', `：${text}`));
    steps.append(item);
  });
  intro.append(steps);
  panel.append(intro);

  const form = el('article', 'card');
  form.append(el('h3', '', '新建任务'));
  const goal = el('textarea');
  goal.rows = 3;
  goal.placeholder = '例如：整理今天的客户邮件，挑出需要我回复的，并写一份回复草稿';
  const goalHint = el('div', 'hint', '一句话写清目标和想要的结果。');
  const title = el('input');
  title.type = 'text';
  title.placeholder = '任务标题（不填会自动取第一句话）';
  const criteria = el('input');
  criteria.type = 'text';
  criteria.placeholder = '完成标准（可选）';
  const profile = el('select');
  [['read', '只查看，不改文件'], ['internal', '可以在工作目录里整理和写文件'], ['external', '可以准备对外草稿（不会自动发送）']].forEach(([value, label]) => {
    const option = el('option', '', label);
    option.value = value;
    profile.append(option);
  });
  const advanced = el('details', 'fold');
  advanced.append(el('summary', '', '高级设置'));
  advanced.append(title, criteria, profile);
  const create = actionButton('开始执行', async () => {
    const goalText = goal.value.trim();
    if (!goalText) throw new Error('请先写一下要做什么');
    const titleText = title.value.trim() ||
      goalText.split(/\n|[。；;]/)[0].trim().slice(0, 40) ||
      '新的长期任务';
    const result = await api.createWorkTask({
      title: titleText, goal: goalText,
      completion_criteria: criteria.value.trim(), profile: profile.value,
      start: true,
    });
    title.value = '';
    goal.value = '';
    criteria.value = '';
    statusEl.textContent = '任务已创建，管家会开始处理';
    if (result && result.task) await loadTaskDetail(result.task.id, true);
  });
  const quick = el('div', 'quick-actions');
  quick.append(create, el('span', 'hint', '不会自动对外发送；需要你确认的步骤会停下来。'));
  form.append(goal, goalHint, advanced, quick);
  panel.append(form);

  const groups = [
    ['执行中', task => ['queued', 'active'].includes(task.status)],
    ['待你处理', task => String(task.status).startsWith('waiting') || task.status === 'pending_acceptance'],
    ['暂停 / 异常', task => ['paused', 'failed'].includes(task.status)],
    ['已完成', task => ['completed', 'cancelled'].includes(task.status)],
  ];
  let shown = 0;
  groups.forEach(([label, match]) => {
    const items = state.workTasks.filter(match).slice(0, 20);
    if (!items.length) return;
    shown += items.length;
    panel.append(el('h2', '', `${label} · ${items.length}`));
    items.forEach(task => panel.append(taskCard(task)));
  });
  if (!shown) panel.append(el('div', 'empty', '还没有任务。在上面的输入框里写一句，就能开始。'));
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

function renderBrief() {
  const panel = panels.brief;
  panel.replaceChildren();
  panel.append(el('div', 'hint', '简报会把今天的重要变化、需要你处理的事和背景整理在一起。可以手动生成，不会打扰聊天。'));
  if (!state.brief) {
    panel.append(el('div', 'empty', state.briefError || '今天还没有简报。'));
    panel.append(actionButton('生成今天的简报', async () => {
      await api.generateBrief(today());
      await refresh();
    }));
    return;
  }
  const brief = state.brief;
  const meta = `${brief.brief_date} · 第 ${brief.version} 版 · ${brief.model || '模型'} · ${brief.status}`;
  const root = card('今日简报', meta, '');
  const pre = el('pre', '', brief.content_md || '');
  root.append(pre);
  const actions = el('div', 'actions');
  actions.append(actionButton('重新生成', async () => {
    await api.generateBrief(today());
    await refresh();
  }, 'secondary'));
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
  const panel = channelPanel;
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
  const panel = webPanel;
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

const WECHAT_STATE_LABELS = {
  off: '已关闭', waiting_window: '未检测到微信', waiting_conversation: '等待打开会话',
  waiting_authorization: '等待授权', observing: '正在观察', degraded: '降级（有缺口）', error: '错误',
};

function renderHealth() {
  if (!healthPanel) return;
  healthPanel.replaceChildren();
  const health = state.health || {};
  const wechat = health.wechat || {};
  const spool = health.spool || {};
  const root = el('article', 'card');
  root.append(el('h3', '', '感知采集状态'));
  const stateLabel = WECHAT_STATE_LABELS[wechat.state] || wechat.state || '未知';
  const lines = [
    `微信：${stateLabel}${wechat.currentSource ? ` · 当前会话「${wechat.currentSource}」` : ''}`,
    `最近观察：${wechat.lastObservedAt ? new Date(wechat.lastObservedAt).toLocaleString() : '尚未观察'}`,
    `最近入库：${wechat.lastDeliveredAt ? new Date(wechat.lastDeliveredAt).toLocaleString() : '尚未确认入库'}`,
    `完整性：${wechat.completeness || 'unknown'} · 待发 ${wechat.pending || 0} · 过滤 ${wechat.filtered || 0} · 失败 ${wechat.failed || 0}`,
  ];
  if (wechat.lastError) lines.push(`最近错误：${wechat.lastError}`);
  root.append(el('div', 'body', lines.join('\n')));

  const actions = el('div', 'actions');
  actions.append(actionButton('测试微信采集', async () => {
    statusEl.textContent = '正在读取微信当前会话…';
    const result = await api.testWeChatCapture();
    state.wechatTest = result;
    statusEl.textContent = result.ok
      ? `采集测试成功：${result.title}，识别 ${result.messageCount} 条可见消息`
      : `采集测试未通过：${result.error || '未知原因'}`;
    renderHealth();
  }, 'secondary'));
  const title = (state.wechatTest && state.wechatTest.title) || wechat.currentSource;
  if (title && (!wechat.enabled || !wechat.authorized)) {
    actions.append(actionButton('启用并允许当前会话', async () => {
      const current = (state.channels && state.channels.configs || [])
        .find(item => item.channel === 'wechat_desktop') || {};
      await api.updateChannelConfig('wechat_desktop', {
        enabled: true,
        summary_enabled: Boolean(current.summary_enabled),
        index_enabled: Boolean(current.index_enabled),
        brief_enabled: Boolean(current.brief_enabled),
        auto_worker_enabled: Boolean(current.auto_worker_enabled),
      });
      await api.updateChannelPolicy({ channel: 'wechat_desktop', source: title, allowed: true });
      await api.refreshChannelCache?.();
      statusEl.textContent = `已启用微信采集，并允许会话「${title}」`;
      await refresh();
    }));
  }
  root.append(actions);

  if (state.wechatTest) {
    const test = state.wechatTest;
    const preview = el('div', 'body');
    preview.append(el('div', 'meta',
      `测试结果：${test.ok ? '通过' : '未通过'} · ${test.title || '未识别会话'} · ${test.authorized ? '已授权' : '未授权'} · ${test.messageCount || 0} 条可见消息`));
    (test.messages || []).slice(0, 4).forEach(message => {
      preview.append(el('div', 'hint', `[${message.direction}] ${message.text}`));
    });
    root.append(preview);
  }

  const browserSites = (health.browser && health.browser.sites || []).length;
  root.append(el('div', 'hint',
    `网页采集：${browserSites} 个站点有检查点；活动片段：${health.activity && health.activity.enabled ? '协作模式开启' : '协作模式关闭'}；待发队列 ${spool.pending || 0} 条。`));
  healthPanel.append(root);
}

function renderAccess() {
  renderHealth();
  renderChannels();
  renderWeb();
}

function render() {
  document.querySelectorAll('nav button').forEach(button => {
    button.classList.toggle('active', button.dataset.tab === state.tab);
  });
  Object.entries(panels).forEach(([name, panel]) => { panel.hidden = name !== state.tab; });
  if (state.tab === 'tasks') renderTasks();
  else if (state.tab === 'pending') renderPending();
  else if (state.tab === 'brief') renderBrief();
  else if (state.tab === 'access') renderAccess();
}

async function refresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(async () => {
    const results = await Promise.allSettled([
      api.listWorkTasks('', 100),
      api.listOutbox('pending_approval'),
      api.latestBrief(today()),
      api.channelSettings(),
      api.getEmailSettings(),
      api.listWebSites(),
      api.connectorHealth ? api.connectorHealth() : Promise.reject(new Error('连接器健康接口不可用')),
    ]);
    if (results[0].status === 'fulfilled') state.workTasks = results[0].value.tasks || [];
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
    if (results[6] && results[6].status === 'fulfilled') state.health = results[6].value;
    if (state.taskDetail) {
      try {
        const detail = await api.getWorkTask(state.taskDetail.task.id);
        const events = await api.listTaskEvents(state.taskDetail.task.id, 0);
        state.taskDetail = detail;
        state.taskEvents = events.events || [];
      } catch {
        // 详情刷新失败不覆盖主列表；下次刷新或操作会重试。
      }
    }
    const actionable = state.workTasks.filter(task =>
      String(task.status).startsWith('waiting') ||
      ['pending_acceptance', 'failed'].includes(task.status)).length;
    const pendingTotal = actionable + state.outbox.length;
    pendingCountEl.hidden = pendingTotal === 0;
    pendingCountEl.textContent = String(pendingTotal);
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

if (api.onAgentEvent) {
  api.onAgentEvent(event => {
    const kind = String(event && event.kind || '');
    if (!kind) return;
    clearTimeout(eventRefresh);
    eventRefresh = setTimeout(refresh, 500);
  });
}

api.listWorkTasks('', 100)
  .then(() => { statusEl.textContent = '已连接'; })
  .catch(error => { statusEl.textContent = `未连接：${error.message}`; });
refresh();
setInterval(refresh, 20000);
