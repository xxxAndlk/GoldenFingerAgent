'use strict';

const api = window.desktopAssistant;
const format = window.ActivityFormat;

const activityEvents = new Map();
const panel = document.querySelector('#activity-panel');
const list = document.querySelector('#activity-list');
const empty = document.querySelector('#activity-empty');
const live = document.querySelector('#activity-live');
const summaryState = document.querySelector('#summary-state');
const summarizeButton = document.querySelector('#summarize-now');
const clearButton = document.querySelector('#clear-activity');
const hideButton = document.querySelector('#activity-hide');
const edgeTab = document.querySelector('#edge-tab');
const toast = document.querySelector('#activity-toast');
const notice = document.querySelector('#activity-notice');
const noticeTitle = document.querySelector('#notice-title');
const noticeMessage = document.querySelector('#notice-message');
const noticeDetail = document.querySelector('#notice-detail');
const noticeToggle = document.querySelector('#notice-detail-toggle');
const noticeClose = document.querySelector('#notice-close');
const evidenceViewer = document.querySelector('#evidence-viewer');
const evidenceImage = document.querySelector('#evidence-image');
const evidenceCaption = document.querySelector('#evidence-caption');

const state = {
  collapsed: false,
  edge: '',
  summarizing: false,
  status: null,
  noticeKey: '',
  noticeCount: 0,
  toastTimer: null,
};

function usefulEvents() {
  return [...activityEvents.values()]
    .filter(format.isUsefulEvent)
    .sort((a, b) => a.at - b.at)
    .slice(-400);
}

function renderRow(event) {
  const meta = format.typeMeta(event.type);
  const item = document.createElement('li');
  item.className = `event-row tone-${meta.tone}`;
  const button = document.createElement('button');
  button.type = 'button';
  button.className = `event tone-${meta.tone}`;
  button.title = [event.title, event.app, event.url, event.captureNote].filter(Boolean).join('\n');

  const time = document.createElement('time');
  time.dateTime = new Date(event.at).toISOString();
  time.textContent = format.formatClock(event.at);

  const rail = document.createElement('span');
  rail.className = 'rail';
  rail.setAttribute('aria-hidden', 'true');

  const body = document.createElement('span');
  body.className = 'event-body';
  const line = document.createElement('span');
  line.className = 'event-line';

  const appName = format.appLabel(event.app);
  const badge = document.createElement('span');
  badge.className = `app-badge tone-${format.appTone(event.app)}`;
  badge.textContent = format.appInitial(event.app);

  const title = document.createElement('strong');
  title.textContent = event.title || event.detail || appName || '未命名窗口';
  line.append(badge, title);

  if (appName && appName !== title.textContent) {
    const app = document.createElement('span');
    app.className = 'app-name';
    app.textContent = appName;
    line.appendChild(app);
  }

  const chip = document.createElement('span');
  chip.className = 'type-chip';
  chip.textContent = meta.label;
  line.appendChild(chip);

  if (event.summarized) {
    const summarizedChip = document.createElement('span');
    summarizedChip.className = 'type-chip summarized';
    summarizedChip.textContent = '已总结';
    if (event.evidenceExpiresAt) {
      summarizedChip.title = `截图保留至 ${new Date(event.evidenceExpiresAt).toLocaleString('zh-CN')}`;
    }
    line.appendChild(summarizedChip);
  }

  const detailText = event.detail && event.detail !== title.textContent
    ? event.detail
    : (event.content ? format.summaryExcerpt(event.content, 180) : '');
  if (detailText) {
    const detail = document.createElement('span');
    detail.className = 'event-detail';
    detail.textContent = detailText;
    body.append(line, detail);
  } else {
    body.appendChild(line);
  }

  const evidence = document.createElement('span');
  evidence.className = 'event-evidence';
  evidence.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 8.4h2.6L8.2 6h7.6l1.6 2.4H20v10H4z"/><circle cx="12" cy="13.2" r="3"/></svg>';
  evidence.style.opacity = event.hasEvidence ? '1' : '.32';

  button.append(time, rail, body, evidence);
  // 没有本地截图时点击只提示，不要弹出「没有本地证据」错误。
  if (event.hasEvidence) button.addEventListener('click', () => openEvidence(event));
  else button.addEventListener('click', () => showToast(
    event.summarized ? '这条记录已总结，截图已过 3 天保留期并清理，只能查看文字' : '这条记录没有本地截图，只能查看文字',
    'info',
  ));
  item.appendChild(button);
  return item;
}

function renderList() {
  const rows = usefulEvents();
  const atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 44;
  const scrollTop = list.scrollTop;
  list.replaceChildren(...rows.map(renderRow));
  empty.hidden = rows.length > 0;
  list.hidden = rows.length === 0;
  if (!rows.length) return;
  list.scrollTop = atBottom ? list.scrollHeight : Math.min(scrollTop, list.scrollHeight);
}

async function openEvidence(event) {
  try {
    const result = await api.activityEvidence(event.id);
    evidenceImage.src = result.dataUrl;
    evidenceCaption.textContent = `${result.event.detail || result.event.title} · ${result.event.app}${result.approximate ? '（邻近时刻补证）' : ''}`;
    evidenceViewer.hidden = false;
  } catch (error) {
    showToast(error.message, 'error');
  }
}

function showToast(message, tone = '') {
  if (!message) return;
  toast.textContent = message;
  toast.className = `activity-toast${tone ? ` ${tone}` : ''}`;
  toast.hidden = false;
  clearTimeout(state.toastTimer);
  state.toastTimer = setTimeout(() => { toast.hidden = true; }, 4200);
}

function showNotice(raw) {
  const noticeData = format.sanitizeError(raw);
  const key = `${noticeData.title}|${noticeData.detail}`;
  if (key === state.noticeKey) state.noticeCount += 1;
  else {
    state.noticeKey = key;
    state.noticeCount = 1;
    noticeDetail.textContent = noticeData.detail || '没有更多信息';
    noticeDetail.hidden = true;
    noticeToggle.textContent = '详情';
  }
  noticeTitle.textContent = noticeData.title;
  noticeMessage.textContent = state.noticeCount > 1
    ? `${noticeData.message}（已出现 ${state.noticeCount} 次）`
    : noticeData.message;
  notice.hidden = false;
}

function hideNotice() {
  notice.hidden = true;
  noticeDetail.hidden = true;
  state.noticeKey = '';
  state.noticeCount = 0;
}

function renderStatus() {
  const status = state.status;
  if (!status) return;
  live.textContent = status.enabled ? '实时' : '未开启';
  live.classList.toggle('live', Boolean(status.enabled));
  summarizeButton.disabled = !status.enabled || state.summarizing;
  summarizeButton.title = status.enabled ? '不用等定时总结，马上总结这一段' : '先在主窗口顶栏开启协作模式';
  if (!status.enabled) {
    summaryState.textContent = '协作模式未开启，不会记录';
    return;
  }
  const parts = [];
  if (status.lastSummaryAt) parts.push(`上次总结 ${format.formatClock(Date.parse(status.lastSummaryAt))}`);
  else parts.push('本阶段尚未总结');
  if (status.nextSummaryAt) parts.push(`下次约 ${format.formatClock(Date.parse(status.nextSummaryAt))}`);
  parts.push(`共 ${status.eventCount || 0} 条`);
  summaryState.textContent = parts.join(' · ');
}

async function refreshStatus() {
  try {
    state.status = await api.collaborationStatus();
    renderStatus();
  } catch {
    summaryState.textContent = '状态读取失败，稍后自动重试';
  }
}

function applyCollapsed(payload) {
  const collapsed = Boolean(payload && payload.collapsed);
  const edge = payload && payload.edge;
  state.collapsed = collapsed;
  state.edge = edge || '';
  document.body.classList.toggle('collapsed', collapsed);
  document.body.dataset.edge = state.edge;
  panel.hidden = collapsed;
  edgeTab.hidden = !collapsed;
  if (collapsed) {
    delete document.body.dataset.dock;
    toast.hidden = true;
    clearTimeout(state.toastTimer);
    evidenceViewer.hidden = true;
  } else {
    document.body.dataset.dock = edge || '';
  }
}

async function summarize() {
  if (state.summarizing) return;
  if (!state.status || !state.status.enabled) {
    showToast('先在主窗口顶栏开启协作模式，再总结', 'info');
    return;
  }
  state.summarizing = true;
  summarizeButton.disabled = true;
  summarizeButton.classList.add('busy');
  summarizeButton.textContent = '总结中…';
  try {
    const result = await api.summarizeNow();
    if (result && result.error) showNotice(result.error);
    else if (!result || !result.reply || /还没有采集到/.test(result.reply)) {
      showToast('本阶段还没有可总结的新内容', 'info');
    } else {
      showToast(`已总结：${format.summaryExcerpt(result.reply, 64)}`, 'ok');
    }
  } catch (error) {
    showNotice(`总结失败：${error.message}`);
  } finally {
    state.summarizing = false;
    summarizeButton.disabled = false;
    summarizeButton.classList.remove('busy');
    summarizeButton.textContent = '立即总结';
    await refreshStatus();
    renderList();
  }
}

function setOpacity(value) {
  document.documentElement.style.setProperty('--activity-opacity', Number(value) / 100);
}

api.onActivity(event => {
  activityEvents.set(event.id, event);
  renderList();
});
api.onActivityError(error => showNotice(error));
api.onActivityCollapsed(payload => applyCollapsed(payload));
api.onActivityOpacity(setOpacity);
async function refreshActivityList() {
  try {
    const events = await api.activityList();
    activityEvents.clear();
    for (const event of events) activityEvents.set(event.id, event);
    renderList();
  } catch { /* 保留当前列表 */ }
}

api.onSummary(summary => {
  if (!state.summarizing) {
    if (summary && summary.error) showNotice(summary.error);
    else if (summary && summary.reply) showToast('定时总结完成，结果已发送到对话', 'ok');
  }
  refreshStatus();
  refreshActivityList();
});

summarizeButton.addEventListener('click', summarize);
hideButton.addEventListener('click', () => api.setActivityVisible(false));
edgeTab.addEventListener('click', () => api.setActivityCollapsed(false));
clearButton.addEventListener('click', async () => {
  await api.clearActivity();
  activityEvents.clear();
  evidenceViewer.hidden = true;
  hideNotice();
  renderList();
  await refreshStatus();
  showToast('已清空本阶段的本地记录', 'ok');
});
noticeToggle.addEventListener('click', () => {
  noticeDetail.hidden = !noticeDetail.hidden;
  noticeToggle.textContent = noticeDetail.hidden ? '详情' : '收起';
});
noticeClose.addEventListener('click', hideNotice);
document.querySelector('#close-evidence').addEventListener('click', () => { evidenceViewer.hidden = true; });

api.activityList().then(events => {
  for (const event of events) activityEvents.set(event.id, event);
  renderList();
}).catch(() => {});
api.getDesktopSettings().then(settings => {
  setOpacity(settings.activityOpacity);
  applyCollapsed({ collapsed: settings.activityCollapsed });
}).catch(() => {});
refreshStatus();
setInterval(refreshStatus, 5000);
