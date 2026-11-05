'use strict';

const api = window.desktopAssistant;
const settingsStatus = document.querySelector('#settings-status');
const opacity = document.querySelector('#activity-opacity');
const opacityValue = document.querySelector('#activity-opacity-value');
const activityVisible = document.querySelector('#activity-visible-toggle');
const modelList = document.querySelector('#model-list');
const modelSummary = document.querySelector('#model-summary');
const searchSummary = document.querySelector('#search-summary');
const searchStatus = document.querySelector('#search-status');
const relaySaved = document.querySelector('#relay-saved');
const modelForm = document.querySelector('#settings-form');
const modelEditorTitle = document.querySelector('#model-editor-title');

// 当前正在编辑的方案 id；空串表示「新建」。
let editingModelId = '';
let activeEnvelope = null;

function fieldValue(id) {
  return document.querySelector(id).value;
}

function setField(id, value) {
  document.querySelector(id).value = value == null ? '' : value;
}

function effortLabel(value) {
  if (value === 'low') return 'low · 快速';
  if (value === 'max') return 'max · 最深入';
  return 'high · 标准';
}

// fillEditor 把一套配置填进编辑表单。
function fillEditor(model) {
  setField('#model-name-display', model.name || '');
  setField('#base-url', model.base_url || '');
  setField('#model-name', model.model || '');
  setField('#api-key', model.api_key || '');
  setField('#protocol', model.protocol || 'responses');
  const effort = model.reasoning_effort === 'low' || model.reasoning_effort === 'max' ? model.reasoning_effort : 'high';
  setField('#reasoning-effort', effort);
  setField('#max-context', model.max_context || '');
  setField('#max-output', model.max_output || '');
}

function openEditor({ id = '', title = '新增模型', model = {} } = {}) {
  editingModelId = id;
  modelEditorTitle.textContent = title;
  fillEditor(model);
  modelForm.hidden = false;
  settingsStatus.textContent = id ? '修改后点「保存并应用」' : '填好配置后点「保存并应用」';
  document.querySelector('#model-name').focus();
}

function closeEditor() {
  editingModelId = '';
  modelForm.hidden = true;
}

// renderModels 渲染可点选切换的模型卡片：点整行即切换为当前使用。
function renderModels(models) {
  modelList.textContent = '';
  const active = models.find(model => model.active);
  if (!active) modelSummary.textContent = '还没有可用的模型';
  else if (active.id) modelSummary.textContent = `当前：${active.name || active.model} · ${active.model}`;
  else modelSummary.textContent = `当前：${active.model}（还没存成方案）`;

  if (!models.length) {
    const empty = document.createElement('li');
    empty.className = 'model-empty';
    empty.textContent = '还没有保存的模型，点下面「新增模型」填第一套配置。';
    modelList.append(empty);
    return;
  }

  for (const model of models) {
    const row = document.createElement('li');
    row.className = `model-row${model.active ? ' active' : ''}`;

    const pick = document.createElement('button');
    pick.type = 'button';
    pick.className = 'model-pick';
    pick.setAttribute('aria-pressed', model.active ? 'true' : 'false');

    const dot = document.createElement('span');
    dot.className = 'model-dot';
    dot.setAttribute('aria-hidden', 'true');

    const info = document.createElement('span');
    info.className = 'model-info';
    const name = document.createElement('strong');
    name.textContent = model.name || model.model;
    const meta = document.createElement('small');
    const parts = [model.model, model.protocol === 'responses' ? 'Responses' : 'Chat Completions'];
    parts.push(effortLabel(model.reasoning_effort));
    if (model.max_output) parts.push(`输出 ${model.max_output}`);
    if (!model.has_key) parts.push('缺少 Key');
    meta.textContent = parts.join(' · ');
    info.append(name, meta);
    pick.append(dot, info);

    pick.addEventListener('click', async () => {
      if (model.active) return;
      pick.disabled = true;
      try {
        replaceEnvelope(await api.activateModel(model.id));
        settingsStatus.textContent = `已切换到「${model.name || model.model}」`;
      } catch (error) {
        settingsStatus.textContent = `切换失败：${error.message}`;
        pick.disabled = false;
      }
    });

    const actions = document.createElement('span');
    actions.className = 'model-actions';

    const edit = document.createElement('button');
    edit.type = 'button';
    edit.className = 'icon-button';
    edit.title = model.id ? '编辑这套配置' : '把当前配置存成一套方案';
    edit.setAttribute('aria-label', '编辑');
    edit.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4.4 19.6h3.4L18.4 9a2.3 2.3 0 0 0-3.2-3.2L4.6 16.4z"/></svg>';
    edit.addEventListener('click', () => {
      document.querySelector('#model-config').open = true;
      openEditor({
        id: model.id || '',
        title: model.id ? `编辑：${model.name || model.model}` : '把当前配置存成方案',
        model,
      });
    });
    actions.append(edit);

    // 占位行（还没保存过任何方案）没有 id，不允许删除。
    if (model.id) {
      const del = document.createElement('button');
      del.type = 'button';
      del.className = 'icon-button danger';
      del.title = '删除这套配置';
      del.setAttribute('aria-label', '删除');
      del.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5.6 7.4h12.8M9.4 7.4V5.2h5.2v2.2M7.2 7.4l.8 11h8l.8-11"/></svg>';
      del.addEventListener('click', async () => {
        del.disabled = true;
        try {
          replaceEnvelope(await api.deleteModel(model.id));
          settingsStatus.textContent = '已删除该配置';
          if (editingModelId === model.id) closeEditor();
        } catch (error) {
          settingsStatus.textContent = `删除失败：${error.message}`;
          del.disabled = false;
        }
      });
      actions.append(del);
    }

    row.append(pick, actions);
    modelList.append(row);
  }
}

// replaceEnvelope 用服务端返回的完整设置刷新整个设置页。
function replaceEnvelope(envelope) {
  activeEnvelope = envelope;
  const models = envelope.models || [];
  renderModels(models);
  // 编辑器收起时也让它保存当前生效的配置：展开「新增」是空白表单，但字段默认值
  // 必须反映真实设置，否则自检和「改了又被空值覆盖」都会出问题。
  if (modelForm.hidden) {
    const active = models.find(model => model.active);
    if (active) fillEditor(active);
    else if (envelope.llm) fillEditor({ ...envelope.llm, name: '' });
  }
  if (envelope.llm) {
    document.querySelector('#settings-path').textContent = envelope.llm.settings_path || '';
  }
  if (envelope.search) {
    setField('#search-base', envelope.search.base_url || 'https://api.deepseek.com');
    setField('#search-key', envelope.search.api_key || '');
    const has = Boolean(envelope.search.has_key);
    searchSummary.textContent = has ? '已使用独立搜索密钥' : '未单独设置，复用模型 Key';
    searchStatus.textContent = has ? '已保存独立搜索密钥（留空不改）' : '未设置独立密钥，将复用模型 Key';
  }
  if (envelope.relay) setField('#relay-url', envelope.relay.url || '');
}

function renderOpacity(value) {
  opacity.value = value;
  opacityValue.value = `${value}%`;
}

async function loadSettings() {
  try {
    replaceEnvelope(await api.getSettings());
  } catch (error) {
    modelSummary.textContent = `读取失败：${error.message}`;
    // 退回旧接口，至少保证当前模型可编辑。
    try {
      const llm = await api.getLLMSettings();
      renderModels([]);
      fillEditor({ ...llm, name: '' });
      document.querySelector('#settings-path').textContent = llm.settings_path || '';
    } catch (inner) {
      modelSummary.textContent = `读取失败：${inner.message}`;
    }
  }
}

// 保存编辑中的配置：有 id 就是更新，没有就是新建并立即启用。
modelForm.addEventListener('submit', async event => {
  event.preventDefault();
  const button = document.querySelector('#save-settings');
  button.disabled = true;
  settingsStatus.textContent = '正在保存…';
  const payload = {
    id: editingModelId,
    name: fieldValue('#model-name-display') || fieldValue('#model-name'),
    base_url: fieldValue('#base-url'),
    model: fieldValue('#model-name'),
    api_key: fieldValue('#api-key'),
    protocol: fieldValue('#protocol'),
    reasoning_effort: fieldValue('#reasoning-effort'),
    max_context: Number(fieldValue('#max-context')) || 0,
    max_output: Number(fieldValue('#max-output')) || 0,
    active: true,
  };
  try {
    replaceEnvelope(await api.saveModel(payload));
    const saved = editingModelId ? '已保存修改并启用' : '已新增并启用';
    closeEditor();
    settingsStatus.textContent = saved;
  } catch (error) {
    settingsStatus.textContent = `保存失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
});

document.querySelector('#model-new').addEventListener('click', () => {
  openEditor();
});

document.querySelector('#model-editor-cancel').addEventListener('click', () => {
  closeEditor();
  settingsStatus.textContent = '';
});

// 模型配置 / 联网搜索两块默认只展开模型配置，避免一屏全是表单。
document.querySelector('#model-config').addEventListener('toggle', event => {
  if (event.target.open) document.querySelector('#search-config').open = false;
});
document.querySelector('#search-config').addEventListener('toggle', event => {
  if (event.target.open) document.querySelector('#model-config').open = false;
});

// ---- 联网搜索密钥 ----
document.querySelector('#search-form').addEventListener('submit', async event => {
  event.preventDefault();
  const button = document.querySelector('#save-search');
  button.disabled = true;
  searchStatus.textContent = '正在保存…';
  try {
    replaceEnvelope(await api.saveSearch({
      base_url: fieldValue('#search-base'),
      api_key: fieldValue('#search-key'),
    }));
    searchStatus.textContent = '已保存，立即生效';
  } catch (error) {
    searchStatus.textContent = `保存失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
});

// ---- 中继地址 ----
document.querySelector('#relay-form').addEventListener('submit', async event => {
  event.preventDefault();
  const button = document.querySelector('#save-relay');
  button.disabled = true;
  relaySaved.textContent = '正在保存…';
  try {
    replaceEnvelope(await api.saveRelay({ url: fieldValue('#relay-url') }));
    relaySaved.textContent = '已保存；重启金手指后生效';
  } catch (error) {
    relaySaved.textContent = `保存失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
});

opacity.addEventListener('input', async () => {
  renderOpacity(opacity.value);
  const settings = await api.setActivityOpacity(opacity.value);
  renderOpacity(settings.activityOpacity);
});
activityVisible.addEventListener('change', async () => {
  activityVisible.checked = await api.setActivityVisible(activityVisible.checked);
});
api.onActivityVisible(value => { activityVisible.checked = value; });

// ---- 工作轨迹：立即总结（不用等 15 分钟定时） ----
const summarizeButton = document.querySelector('#summarize-now-settings');
const summarizeStatus = document.querySelector('#summarize-status');
summarizeButton?.addEventListener('click', async () => {
  summarizeButton.disabled = true;
  summarizeStatus.textContent = '正在总结…';
  try {
    const result = await api.summarizeNow();
    if (result?.error) summarizeStatus.textContent = result.error;
    else if (result?.reply) summarizeStatus.textContent = '已总结，结果已发送到对话窗口';
    else summarizeStatus.textContent = '本阶段还没有可总结的新内容';
  } catch (error) {
    summarizeStatus.textContent = `总结失败：${error.message}`;
  } finally {
    summarizeButton.disabled = false;
  }
});

// ---- 手机生活管家：配对码与已绑定手机 ----
const relayStatusText = document.querySelector('#relay-status');
const relayPairButton = document.querySelector('#relay-pair-code');
const relayCodeText = document.querySelector('#relay-code');
const relayChannelList = document.querySelector('#relay-channels');

function renderChannels(channels) {
  relayChannelList.textContent = '';
  for (const channel of channels) {
    const item = document.createElement('li');
    const label = document.createElement('span');
    label.textContent = `${channel.phone_name || '手机'} · 配对于 ${String(channel.created_at || '').slice(0, 10)}`;
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = '解除绑定';
    button.addEventListener('click', async () => {
      button.disabled = true;
      try {
        await api.revokeRelayChannel(channel.channel_id);
        await loadRelayChannels();
      } catch (error) {
        relayStatusText.textContent = `解除失败：${error.message}`;
        button.disabled = false;
      }
    });
    item.append(label, button);
    relayChannelList.append(item);
  }
}

async function loadRelayChannels() {
  try {
    const result = await api.relayChannels();
    renderChannels(result.channels || []);
  } catch (error) {
    renderChannels([]);
  }
}

async function loadRelayStatus() {
  try {
    const status = await api.relayStatus();
    if (!status.configured) {
      relayStatusText.textContent = '未启用：服务启动时未配置中继地址';
      relayPairButton.disabled = true;
      return;
    }
    relayStatusText.textContent = status.connected ? '已连接中继' : '中继离线，正在重连…';
    relayPairButton.disabled = false;
    await loadRelayChannels();
  } catch (error) {
    relayStatusText.textContent = `手机连接不可用：${error.message}`;
    relayPairButton.disabled = true;
  }
}

relayPairButton.addEventListener('click', async () => {
  relayPairButton.disabled = true;
  relayStatusText.textContent = '正在生成配对码…';
  try {
    const result = await api.relayPairCode();
    relayCodeText.hidden = false;
    relayCodeText.textContent = result.code;
    const minutes = Math.max(1, Math.round((new Date(result.expires_at).getTime() - Date.now()) / 60000));
    relayStatusText.textContent = `配对码 ${result.code}，约 ${minutes} 分钟内有效，在手机上输入即可绑定`;
  } catch (error) {
    relayCodeText.hidden = true;
    relayStatusText.textContent = `生成失败：${error.message}`;
  } finally {
    relayPairButton.disabled = false;
  }
});

async function loadSkills() {
  const list = document.querySelector('#skill-list');
  if (!list) return;
  try {
    const result = await api.listSkills();
    list.replaceChildren();
    const skills = (result && result.skills) || [];
    if (!skills.length) {
      const empty = document.createElement('li');
      empty.className = 'skill-item';
      empty.textContent = '还没有安装技能（在 skills/ 下放置 <name>/SKILL.md）。';
      list.append(empty);
      return;
    }
    for (const item of skills) {
      const row = document.createElement('li');
      row.className = 'skill-item';
      const name = document.createElement('strong');
      name.textContent = item.name;
      const description = document.createElement('p');
      description.textContent = item.description;
      row.append(name, description);
      list.append(row);
    }
  } catch (error) {
    list.textContent = `技能读取失败：${error.message}`;
  }
}

// ---- 云端共享记忆 ----
const cloudForm = document.querySelector('#cloud-memory-form');
const cloudEnabled = document.querySelector('#cloud-memory-enabled');
const cloudStatus = document.querySelector('#cloud-memory-status');
const cloudTestButton = document.querySelector('#cloud-memory-test');

function renderCloudMemory(config) {
  if (!cloudForm) return;
  cloudEnabled.checked = Boolean(config.enabled);
  setField('#cloud-memory-url', config.base_url || '');
  setField('#cloud-memory-device', config.device_id || '');
  setField('#cloud-memory-owner', config.owner_user_id || '');
  setField('#cloud-memory-token', config.token || '');
  document.querySelector('#cloud-memory-delivery').checked = Boolean(config.delivery_enabled);
  const mode = config.mode === 'cloud' ? '云端共享模式' : '本机模式';
  const sync = config.mode === 'cloud'
    ? ` · ${config.ready ? '已连接' : '未连接'} · 待发 ${config.pending || 0} / 失败 ${config.failed || 0} / 暂停 ${config.paused || 0}`
    : '';
  const detail = config.detail ? ` · ${config.detail}` : '';
  cloudStatus.textContent = `${mode}${sync}${detail}`;
}

async function loadCloudMemory() {
  if (!cloudForm || typeof api.getCloudMemory !== 'function') return;
  try {
    renderCloudMemory(await api.getCloudMemory());
  } catch (error) {
    cloudStatus.textContent = `读取失败：${error.message}`;
  }
}

if (cloudForm) {
  cloudForm.addEventListener('submit', async event => {
    event.preventDefault();
    const payload = {
      enabled: cloudEnabled.checked,
      base_url: fieldValue('#cloud-memory-url').trim(),
      device_id: fieldValue('#cloud-memory-device').trim(),
      owner_user_id: fieldValue('#cloud-memory-owner').trim(),
      token: fieldValue('#cloud-memory-token'),
      delivery_enabled: document.querySelector('#cloud-memory-delivery').checked,
    };
    try {
      const saved = await api.saveCloudMemory(payload);
      renderCloudMemory(saved);
      cloudStatus.textContent += ' · 已保存，重启应用后生效';
    } catch (error) {
      cloudStatus.textContent = `保存失败：${error.message}`;
    }
  });

  cloudTestButton.addEventListener('click', async () => {
    cloudTestButton.disabled = true;
    cloudStatus.textContent = '正在测试连接…';
    try {
      const result = await api.testCloudMemory({
        base_url: fieldValue('#cloud-memory-url').trim(),
        token: fieldValue('#cloud-memory-token'),
      });
      cloudStatus.textContent = result.ready
        ? `连接成功 · ${result.backend || '云端记忆'}${result.detail ? ' · ' + result.detail : ''}`
        : `连接失败：${result.error || result.detail || '未知错误'}`;
    } catch (error) {
      cloudStatus.textContent = `连接失败：${error.message}`;
    } finally {
      cloudTestButton.disabled = false;
    }
  });
}

loadRelayStatus();
loadSettings();
loadSkills();
loadCloudMemory();
api.getDesktopSettings().then(settings => {
  renderOpacity(settings.activityOpacity);
  activityVisible.checked = settings.activityVisible;
});
