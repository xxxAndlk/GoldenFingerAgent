'use strict';

const api = window.desktopAssistant;
const orb = document.querySelector('#orb');
const panel = document.querySelector('#panel');
const messages = document.querySelector('#messages');
const form = document.querySelector('#chat-form');
const input = document.querySelector('#chat-input');
const send = document.querySelector('#send');
const connection = document.querySelector('#connection');
const orbDot = document.querySelector('#orb-dot');
const collabToggle = document.querySelector('#main-collab-toggle');
const collabStatus = document.querySelector('#main-collab-status');
const themeToggle = document.querySelector('#theme-toggle');
const chatStatus = document.querySelector('#chat-status');
const chatStatusText = document.querySelector('#chat-status-text');
const historyToggle = document.querySelector('#history-toggle');
const historyPanel = document.querySelector('#history-panel');
const historyList = document.querySelector('#history-list');
const historyDate = document.querySelector('#history-date');
const historyNew = document.querySelector('#history-new');
const historyClose = document.querySelector('#history-close');
const workbenchToggle = document.querySelector('#workbench-toggle');
const workbenchDot = document.querySelector('#workbench-dot');
const pendingStrip = document.querySelector('#pending-strip');
const pendingStripText = document.querySelector('#pending-strip-text');
const briefCard = document.querySelector('#brief-card');
const briefCardText = document.querySelector('#brief-card-text');
const historyClearDate = document.querySelector('#history-clear-date');
const attachButton = document.querySelector('#attach');
const attachInput = document.querySelector('#attach-input');
const attachmentStrip = document.querySelector('#attachments');
const summaryLine = document.querySelector('#summary-line');
const summaryCount = document.querySelector('#summary-count');
const summaryModal = document.querySelector('#summary-modal');
const summaryList = document.querySelector('#summary-list');
const summaryClose = document.querySelector('#summary-close');
const UPLOAD_BASE = 'http://127.0.0.1:8080';
const SESSION_KEY = 'gfa.chatSessionId';
const WELCOME = '我是金手指管家，可以对话、记录和调度其他 Agent。要安排长期任务时，点右上角「工作台」；待办和验收也会在那里提醒你。';
let expanded = false;
let pendingReply = null;
let historySessions = [];
let pendingAttachments = [];
let summaryPhases = [];
let summaryTexts = new Set();
const MESSAGE_PAGE_SIZE = 50;
let conversationSession = '';
let oldestMessageId = '';
let messageHasMore = false;
let loadingOlderMessages = false;
let allowOlderLoad = false;

// ---- 对话状态：思考中 / 调用工具 / 等待 Agent ----
const TOOL_LABELS = {
  web_search: '正在联网检索',
  web_fetch: '正在读取网页',
  read: '正在读取文件',
  write: '正在写入文件',
  bash: '正在执行命令',
  delegate_agent: '正在等待 Agent 完成',
  list_agents: '正在读取 Agent 配置',
  refresh_agents: '正在重新扫描 Agent',
  open_agent: '正在唤起 Agent',
  memory_search: '正在检索长期记忆',
  remember: '正在写入长期记忆'
};
const WAITING_TOOLS = new Set(['delegate_agent', 'open_agent']);

function setChatStatus(state, text) {
  setOrbBusy(Boolean(state));
  // 对话进行中只更新同一个思考气泡，避免气泡和底部状态条重复出现。
  if (pendingReply) {
    const label = pendingReply.querySelector('.thinking-text');
    if (label) label.textContent = text || '';
    pendingReply.dataset.state = state || '';
    if (chatStatus) chatStatus.hidden = true;
    return;
  }
  if (!chatStatus) return;
  if (!state) {
    chatStatus.hidden = true;
    chatStatus.className = 'chat-status';
    return;
  }
  chatStatus.hidden = false;
  chatStatus.className = `chat-status ${state}`;
  chatStatusText.textContent = text;
}

function addThinkingMessage(at, createdAt) {
  const element = addMessage('', 'assistant', [], at, createdAt);
  element.classList.add('pending');
  element.dataset.state = 'thinking';
  const line = document.createElement('span');
  line.className = 'thinking-line';
  const dots = document.createElement('span');
  dots.className = 'thinking-dots';
  dots.setAttribute('aria-hidden', 'true');
  dots.append(document.createElement('i'), document.createElement('i'), document.createElement('i'));
  const label = document.createElement('span');
  label.className = 'thinking-text';
  label.textContent = '正在思考…';
  line.append(dots, label);
  element.replaceChildren(line);
  return element;
}

// 悬浮球运行状态：思考/调用工具/等待 Agent 时显示旋转光环。
function setOrbBusy(busy) {
  orb.classList.toggle('busy', busy);
  orb.setAttribute('aria-busy', busy ? 'true' : 'false');
}

function describeStatus(event) {
  if (!event) return ['thinking', '正在思考…'];
  if (event.kind === 'thinking') {
    return ['thinking', event.step > 0 ? '正在整理结果…' : '正在思考…'];
  }
  if (event.kind === 'tool') {
    const label = TOOL_LABELS[event.name] || `正在调用 ${event.name}`;
    return [WAITING_TOOLS.has(event.name) ? 'waiting' : 'tool', `${label}…`];
  }
  if (event.kind === 'tool_done') {
    if (event.detail === 'error') return ['tool', `${event.name} 出错，正在换一种做法…`];
    return ['thinking', '正在整理结果…'];
  }
  if (event.kind === 'clarify') return ['thinking', '需要你确认一下…'];
  return null;
}

function renderExpanded(next) {
  expanded = next;
  orb.hidden = next;
  panel.hidden = !next;
  document.body.dataset.expanded = next ? '1' : '0';
  if (!next) orbDot.hidden = true;
  if (!next) closeHistory();
  if (next) setTimeout(() => input.focus(), 180);
}

// 贴边状态由主进程在拖动停下后通知：收口圆角并显示边缘滑轨。
function renderDock(edge) {
  const dock = edge || '';
  document.body.dataset.dock = dock;
  orb.dataset.dock = dock;
}

function setExpanded(next) {
  renderExpanded(next);
  api.toggle(next);
}

// ---- 总结：单行入口 + 弹窗，不把总结混进普通消息气泡 ----
function currentSessionId() { return localStorage.getItem(SESSION_KEY) || ''; }

function createSummaryMarker(count) {
  const marker = document.createElement('button');
  marker.type = 'button';
  marker.className = 'summary-line summary-flow';
  marker.innerHTML = '<span class="summary-line-dot" aria-hidden="true"></span><span>已产生 <strong></strong> 条总结，点击查看</span><span class="summary-line-arrow" aria-hidden="true">›</span>';
  marker.querySelector('strong').textContent = String(count);
  marker.title = `查看 ${count} 条阶段总结`;
  marker.addEventListener('click', openSummaryModal);
  return marker;
}

// 总结入口是对话流里的节点：放在最近一次总结的时间位置；后续消息自然把它往上顶。
function renderSummaryMarker() {
  const existing = messages.querySelector('.summary-flow');
  if (existing) existing.remove();
  if (!summaryPhases.length) {
    summaryLine.hidden = true;
    return;
  }
  const latest = summaryPhases[summaryPhases.length - 1];
  const latestAt = Number(latest.completedAt || latest.endedAt || 0);
  const label = latestAt ? clockLabel(new Date(latestAt)) : '';
  const fragment = document.createDocumentFragment();
  if (label) fragment.appendChild(buildTimeLabel(label));
  fragment.appendChild(createSummaryMarker(summaryPhases.length));
  let before = null;
  for (const child of messages.children) {
    if (!child.classList || !child.classList.contains('message')) continue;
    const at = Date.parse(child.dataset.createdAt || '');
    if (latestAt && Number.isFinite(at) && at > latestAt) {
      before = child.previousElementSibling?.classList.contains('message-time')
        ? child.previousElementSibling
        : child;
      break;
    }
  }
  const atBottom = messages.scrollHeight - messages.scrollTop - messages.clientHeight < 60;
  messages.insertBefore(fragment, before);
  if (atBottom) messages.scrollTop = messages.scrollHeight;
  summaryLine.hidden = true;
}

async function openSummaryModal() {
  await loadSummaries();
  renderSummaryModal();
  summaryModal.hidden = false;
}

function renderSummaryModal() {
  summaryList.replaceChildren();
  if (!summaryPhases.length) {
    const empty = document.createElement('p');
    empty.className = 'summary-empty';
    empty.textContent = '这个会话还没有阶段总结。';
    summaryList.append(empty);
    return;
  }
  for (const phase of summaryPhases.slice().reverse()) {
    const item = document.createElement('article');
    item.className = 'summary-item';
    const time = document.createElement('div');
    time.className = 'summary-time';
    const at = new Date(phase.completedAt || phase.endedAt || Date.now());
    const when = Number.isNaN(at.getTime()) ? '' : at.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    const records = Array.isArray(phase.eventIds) ? phase.eventIds.length : 0;
    time.textContent = [when, records ? `${records} 条记录` : ''].filter(Boolean).join(' · ');
    const body = document.createElement('div');
    body.className = 'summary-body';
    body.innerHTML = window.MarkdownLite.render(phase.summary || phase.record || '');
    item.append(time, body);
    summaryList.append(item);
  }
}

async function loadSummaries() {
  try {
    summaryPhases = await api.activitySummaries(currentSessionId());
  } catch {
    summaryPhases = [];
  }
  summaryTexts = new Set(summaryPhases.map(phase => String(phase.summary || '').trim()).filter(Boolean));
  renderSummaryMarker();
  if (!summaryModal.hidden) renderSummaryModal();
}

function buildMessage(text, role = 'assistant', attachments = []) {
  const element = document.createElement('article');
  element.className = `message ${role}`;
  if (role === 'assistant') element.innerHTML = window.MarkdownLite.render(text);
  else element.textContent = text;
  if (attachments.length) {
    const box = document.createElement('div');
    box.className = 'message-attachments';
    for (const item of attachments) {
      const src = String(item.url || '').startsWith('/') ? UPLOAD_BASE + item.url : item.url;
      if (!src) continue;
      if (item.kind === 'video') {
        const video = document.createElement('video');
        video.src = src;
        video.controls = true;
        video.preload = 'metadata';
        box.appendChild(video);
      } else {
        const image = document.createElement('img');
        image.src = src;
        image.alt = item.name || '图片';
        box.appendChild(image);
      }
    }
    element.appendChild(box);
  }
  return element;
}

function clockLabel(date = new Date()) {
  return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
}

function buildTimeLabel(at) {
  const label = document.createElement('div');
  label.className = 'message-time';
  label.textContent = at;
  return label;
}

function buildMessageEntry(text, role = 'assistant', attachments = [], at = '', createdAt = '') {
  const fragment = document.createDocumentFragment();
  if (at) fragment.appendChild(buildTimeLabel(at));
  const element = buildMessage(text, role, attachments);
  if (createdAt) element.dataset.createdAt = createdAt;
  fragment.appendChild(element);
  return { fragment, element };
}

function addMessage(text, role = 'assistant', attachments = [], at = '', createdAt = '') {
  const entry = buildMessageEntry(text, role, attachments, at, createdAt);
  messages.appendChild(entry.fragment);
  messages.scrollTop = messages.scrollHeight;
  return entry.element;
}

function renderAttachments() {
  attachmentStrip.replaceChildren();
  attachmentStrip.hidden = pendingAttachments.length === 0;
  pendingAttachments.forEach((item, index) => {
    const chip = document.createElement('div');
    chip.className = 'attachment-chip';
    const src = String(item.url || '').startsWith('/') ? UPLOAD_BASE + item.url : item.url;
    if (item.kind === 'video') {
      const video = document.createElement('video');
      video.src = src;
      video.muted = true;
      video.preload = 'metadata';
      chip.appendChild(video);
    } else {
      const image = document.createElement('img');
      image.src = src;
      image.alt = item.name || '图片';
      chip.appendChild(image);
    }
    const tag = document.createElement('span');
    tag.className = 'attachment-kind';
    tag.textContent = item.kind === 'video' ? '视频' : '图片';
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'attachment-remove';
    remove.setAttribute('aria-label', '移除附件');
    remove.textContent = '×';
    remove.addEventListener('click', () => {
      pendingAttachments.splice(index, 1);
      renderAttachments();
    });
    chip.append(tag, remove);
    attachmentStrip.appendChild(chip);
  });
}

function fileToDataUrl(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ''));
    reader.onerror = () => reject(reader.error || new Error('读取文件失败'));
    reader.readAsDataURL(file);
  });
}

async function addAttachmentFiles(files) {
  for (const file of Array.from(files || [])) {
    if (pendingAttachments.length >= 6) {
      addMessage('最多同时添加 6 个附件', 'system', [], clockLabel(), new Date().toISOString());
      break;
    }
    const kind = file.type.startsWith('image/') ? 'image' : (file.type.startsWith('video/') ? 'video' : '');
    if (!kind) continue;
    if (file.size > 60 * 1024 * 1024) continue;
    attachButton.disabled = true;
    try {
      const data = await fileToDataUrl(file);
      const uploaded = await api.uploadAttachment({ name: file.name, mime: file.type, kind, data });
      pendingAttachments.push(uploaded);
      renderAttachments();
    } catch (error) {
      addMessage(`附件上传失败：${error.message}`, 'error', [], clockLabel(), new Date().toISOString());
    } finally {
      attachButton.disabled = false;
    }
  }
}

function renderCollaboration(status) {
  collabToggle.checked = status.enabled;
  collabStatus.textContent = status.enabled
    ? `正在观察 · ${status.eventCount || 0} 条记录`
    : '未授权，不会观察';
}

async function refreshCollaboration() {
  try { renderCollaboration(await api.collaborationStatus()); }
  catch (error) { collabStatus.textContent = error.message; }
}

async function refreshStatus() {
  const status = await api.status();
  connection.textContent = status.connected ? '已连接本机管家' : '管家服务未连接';
  connection.className = `connection ${status.connected ? 'online' : 'offline'}`;
}

// 主会话的任务卡摘要：待审批、等待澄清、异常结束。
async function refreshWorkbenchBadge() {
  try {
    const [tasksResult, outboxResult] = await Promise.all([
      api.listWorkTasks('', 100),
      api.listOutbox('pending_approval'),
    ]);
    const tasks = (tasksResult && tasksResult.tasks) || [];
    const outbox = (outboxResult && outboxResult.outbox) || [];
    const waiting = tasks.filter(task =>
      String(task.status).startsWith('waiting') ||
      ['pending_acceptance', 'failed'].includes(task.status));
    const count = outbox.length + waiting.length;
    workbenchDot.hidden = count === 0;
    pendingStrip.hidden = count === 0;
    if (count > 0) {
      pendingStripText.textContent = `有 ${count} 项待处理：${outbox.length} 个审批 · ${waiting.length} 个任务`;
    }
  } catch {
    workbenchDot.hidden = true;
    pendingStrip.hidden = true;
  }
}

function localDateString() {
  const now = new Date();
  const pad = value => String(value).padStart(2, '0');
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

async function refreshBriefCard() {
  try {
    const result = await api.latestBrief(localDateString());
    const brief = result && result.brief;
    if (!brief) throw new Error('not ready');
    const text = String(brief.content_md || '')
      .split('\n')
      .map(line => line.replace(/^#+\s*/, '').trim())
      .filter(Boolean)
      .join(' · ');
    briefCardText.textContent = text.length > 90 ? `${text.slice(0, 90)}…` : text;
    briefCard.hidden = false;
  } catch {
    briefCard.hidden = true;
  }
}

workbenchToggle?.addEventListener('click', () => api.openWorkbench());
pendingStrip?.addEventListener('click', () => api.openWorkbench());
briefCard?.addEventListener('click', () => api.openWorkbench());
api.onAgentEvent?.(() => {
  clearTimeout(refreshWorkbenchBadge.timer);
  refreshWorkbenchBadge.timer = setTimeout(refreshWorkbenchBadge, 600);
  clearTimeout(refreshBriefCard.timer);
  refreshBriefCard.timer = setTimeout(refreshBriefCard, 600);
});

collabToggle.addEventListener('change', async () => {
  collabToggle.disabled = true;
  try {
    renderCollaboration(await api.setCollaboration(collabToggle.checked));
  } catch (error) {
    collabToggle.checked = !collabToggle.checked;
    collabStatus.textContent = `切换失败：${error.message}`;
  } finally {
    collabToggle.disabled = false;
  }
});

api.onExpanded(renderExpanded);
api.onWidgetDock?.(renderDock);
api.onSummary(summary => {
  if (summary.error) addMessage(summary.error, 'error', [], clockLabel(), new Date().toISOString());
  if (summary.reply) {
    loadSummaries();
    if (!expanded) orbDot.hidden = false;
  }
});
api.onAgentResult(({ name, result }) => {
  addMessage(`**${name} 执行结果**\n\n${result.output || '任务已提交。'}`, 'assistant', [], clockLabel(), new Date().toISOString());
  if (!expanded) orbDot.hidden = false;
});

// ---- 对话历史：按日期分组，会话名就是创建时间 ----
function pad2(value) { return String(value).padStart(2, '0'); }
function dayKey(date) {
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
}
function dayLabel(date) {
  const now = new Date();
  const key = dayKey(date);
  if (key === dayKey(now)) return `今天 · ${date.getMonth() + 1}月${date.getDate()}日`;
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
  if (key === dayKey(yesterday)) return `昨天 · ${date.getMonth() + 1}月${date.getDate()}日`;
  if (date.getFullYear() === now.getFullYear()) return `${date.getMonth() + 1}月${date.getDate()}日`;
  return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
}
function timeLabel(date) { return `${pad2(date.getHours())}:${pad2(date.getMinutes())}`; }
function sessionDate(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? new Date() : date;
}

function conversationItems(list) {
  return (list || []).filter(item => {
    if (item.role !== 'user' && item.role !== 'assistant') return false;
    // 旧版本曾把总结写成普通助手消息；阶段库里的总结文本在这里过滤掉。
    return !(item.role === 'assistant' && summaryTexts.has(String(item.content || '').trim()));
  });
}

function renderConversation(list, { hasMore = false } = {}) {
  const raw = Array.isArray(list) ? list : [];
  oldestMessageId = raw.length ? String(raw[0].id || '') : '';
  messageHasMore = hasMore;
  messages.replaceChildren();
  allowOlderLoad = false;
  const items = conversationItems(raw);
  if (!items.length) {
    addMessage(WELCOME, 'assistant');
  } else {
    for (const item of items) {
      addMessage(item.content || '', item.role, [], item.at || '', item.created_at || '');
    }
  }
  renderSummaryMarker();
  requestAnimationFrame(() => { allowOlderLoad = true; });
}

// 滚到顶部时加载更早一页；只插入旧消息，不重绘已有 DOM，保留当前滚动位置。
async function loadOlderMessages() {
  if (!conversationSession || !messageHasMore || loadingOlderMessages || !oldestMessageId) return;
  loadingOlderMessages = true;
  allowOlderLoad = false;
  try {
    const previousHeight = messages.scrollHeight;
    const previousTop = messages.scrollTop;
    const page = await api.sessionMessages(conversationSession, oldestMessageId, MESSAGE_PAGE_SIZE);
    if (!Array.isArray(page) || !page.length) {
      messageHasMore = false;
      return;
    }
    oldestMessageId = String(page[0].id || oldestMessageId);
    messageHasMore = page.length >= MESSAGE_PAGE_SIZE;
    const fragment = document.createDocumentFragment();
    for (const item of conversationItems(page)) {
      fragment.appendChild(buildMessageEntry(item.content || '', item.role, [], item.at || '', item.created_at || '').fragment);
    }
    messages.insertBefore(fragment, messages.firstChild);
    renderSummaryMarker();
    // 加载更早消息时必须瞬时锚定；CSS 的 smooth 会把 scrollTop 变成动画，旧内容会跳到顶部。
    const previousBehavior = messages.style.scrollBehavior;
    messages.style.scrollBehavior = 'auto';
    messages.scrollTop = messages.scrollHeight - previousHeight + previousTop;
    requestAnimationFrame(() => { messages.style.scrollBehavior = previousBehavior; });
  } catch {
    // 网络失败保持当前位置，下次滚动再试。
  } finally {
    loadingOlderMessages = false;
    allowOlderLoad = true;
  }
}

messages.addEventListener('scroll', () => {
  if (allowOlderLoad && messages.scrollTop <= 24) loadOlderMessages();
});

function setHistoryMessage(text) {
  historyList.replaceChildren();
  const paragraph = document.createElement('p');
  paragraph.className = 'history-empty';
  paragraph.textContent = text;
  historyList.append(paragraph);
}

function renderHistory() {
  historyList.replaceChildren();
  if (!historySessions.length) {
    setHistoryMessage(historyDate.value ? '这一天没有对话记录' : '还没有对话记录');
    return;
  }
  const current = localStorage.getItem(SESSION_KEY);
  let lastKey = '';
  for (const item of historySessions) {
    const date = sessionDate(item.created_at);
    const key = dayKey(date);
    if (key !== lastKey) {
      const heading = document.createElement('div');
      heading.className = 'history-day';
      heading.textContent = dayLabel(date);
      historyList.append(heading);
      lastKey = key;
    }
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'history-item';
    button.dataset.sessionId = item.id;
    if (item.id === current) button.classList.add('active');
    const time = document.createElement('span');
    time.className = 'history-time';
    time.textContent = timeLabel(date);
    const preview = document.createElement('span');
    preview.className = 'history-preview';
    preview.textContent = item.preview || '（无文字内容）';
    const count = document.createElement('span');
    count.className = 'history-count';
    count.textContent = `${item.messages || 0} 条`;
    button.append(time, preview, count);
    button.title = `${date.toLocaleString()} · ${item.preview || ''}`;
    button.addEventListener('click', () => openSession(item.id));
    historyList.append(button);
  }
}

async function loadHistory() {
  setHistoryMessage('正在读取…');
  try {
    const result = await api.listSessions(historyDate.value || '');
    historySessions = result.sessions || [];
    renderHistory();
  } catch (error) {
    setHistoryMessage(`读取失败：${error.message}`);
  }
}

function openHistory() {
  historyPanel.hidden = false;
  historyToggle.classList.add('active');
  historyDate.value = '';
  loadHistory();
}

function closeHistory() {
  historyPanel.hidden = true;
  historyToggle.classList.remove('active');
}

async function openSession(id) {
  setHistoryMessage('正在打开…');
  try {
    await api.useSession(id);
    localStorage.setItem(SESSION_KEY, id);
    conversationSession = id;
    await loadSummaries();
    const page = await api.sessionMessages(id, '', MESSAGE_PAGE_SIZE);
    renderConversation(page, { hasMore: page.length >= MESSAGE_PAGE_SIZE });
    closeHistory();
    if (!expanded) setExpanded(true);
    input.focus();
  } catch (error) {
    setHistoryMessage(`打开失败：${error.message}`);
  }
}

async function startNewChat() {
  await restoreMainSession();
  closeHistory();
  input.focus();
}

// 桌面与手机共用同一条主会话；旧聊天记录只作为历史查看。
async function restoreMainSession() {
  try {
    const result = await api.mainSession();
    const id = result && result.session && result.session.id;
    if (!id) throw new Error('main session missing');
    await api.useSession(id);
    conversationSession = id;
    localStorage.setItem(SESSION_KEY, id);
    await loadSummaries();
    const page = await api.sessionMessages(id, '', MESSAGE_PAGE_SIZE);
    renderConversation(page, { hasMore: page.length >= MESSAGE_PAGE_SIZE });
  } catch (error) {
    localStorage.removeItem(SESSION_KEY);
    await api.useSession('');
    conversationSession = '';
    oldestMessageId = '';
    messageHasMore = false;
    await loadSummaries();
    renderConversation([]);
  }
}

historyToggle.addEventListener('click', openHistory);
historyClose.addEventListener('click', closeHistory);
historyNew.addEventListener('click', startNewChat);
historyClearDate.addEventListener('click', () => {
  historyDate.value = '';
  loadHistory();
});
historyDate.addEventListener('change', loadHistory);
summaryLine.addEventListener('click', openSummaryModal);
summaryClose.addEventListener('click', () => { summaryModal.hidden = true; });
summaryModal.addEventListener('click', event => {
  if (event.target === summaryModal) summaryModal.hidden = true;
});
document.addEventListener('keydown', event => {
  if (event.key !== 'Escape') return;
  if (!summaryModal.hidden) {
    summaryModal.hidden = true;
    return;
  }
  if (!historyPanel.hidden) closeHistory();
});

let orbDrag = null;
let suppressOrbClick = false;
orb.addEventListener('pointerdown', event => {
  if (event.button !== 0) return;
  orbDrag = { x: event.screenX, y: event.screenY, pointerId: event.pointerId, moved: false };
  suppressOrbClick = false;
  if (orb.setPointerCapture) orb.setPointerCapture(event.pointerId);
  api.dragStart?.(event.screenX, event.screenY);
});
orb.addEventListener('pointermove', event => {
  if (!orbDrag || event.pointerId !== orbDrag.pointerId) return;
  const dx = event.screenX - orbDrag.x;
  const dy = event.screenY - orbDrag.y;
  if (!orbDrag.moved && Math.hypot(dx, dy) >= 4) orbDrag.moved = true;
  if (orbDrag.moved) {
    suppressOrbClick = true;
    api.dragMove?.(event.screenX, event.screenY);
  }
});
function finishOrbDrag(event) {
  if (!orbDrag || event.pointerId !== orbDrag.pointerId) return;
  suppressOrbClick = orbDrag.moved;
  orbDrag = null;
  api.dragEnd?.();
}
orb.addEventListener('pointerup', finishOrbDrag);
orb.addEventListener('pointercancel', finishOrbDrag);
orb.addEventListener('click', event => {
  if (suppressOrbClick) {
    suppressOrbClick = false;
    event.preventDefault();
    return;
  }
  setExpanded(true);
});
document.querySelector('#collapse').addEventListener('click', () => setExpanded(false));
document.querySelector('#agents-toggle').addEventListener('click', () => api.openSettings());

attachButton.addEventListener('click', () => attachInput.click());
attachInput.addEventListener('change', async () => {
  const files = attachInput.files;
  attachInput.value = '';
  await addAttachmentFiles(files);
});

form.addEventListener('submit', async event => {
  event.preventDefault();
  const text = input.value.trim();
  const files = pendingAttachments.slice();
  if (!text && !files.length) return;
  input.value = '';
  send.disabled = true;
  const sentAt = new Date();
  addMessage(text || '（附件）', 'user', files, clockLabel(sentAt), sentAt.toISOString());
  pendingReply = addThinkingMessage(clockLabel(sentAt), sentAt.toISOString());
  setChatStatus('thinking', '正在思考…');
  try {
    const response = await api.chat(text, files);
    if (response.session_id) {
      if (!conversationSession) {
        conversationSession = response.session_id;
        oldestMessageId = '';
        messageHasMore = false;
      }
      localStorage.setItem(SESSION_KEY, response.session_id);
      loadSummaries();
    }
    pendingAttachments = [];
    renderAttachments();
    pendingReply.classList.remove('pending');
    delete pendingReply.dataset.state;
    pendingReply.innerHTML = window.MarkdownLite.render(response.reply || '我没有收到回答。');
    pendingReply = null;
  } catch (error) {
    pendingReply.classList.remove('pending');
    delete pendingReply.dataset.state;
    pendingReply.textContent = `发送失败：${error.message}`;
    pendingReply.classList.add('error');
    pendingReply = null;
  } finally {
    setChatStatus(null);
    send.disabled = false;
    input.focus();
  }
});

input.addEventListener('keydown', event => {
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault();
    form.requestSubmit();
  }
});

refreshStatus();
refreshCollaboration();
refreshWorkbenchBadge();
refreshBriefCard();
setInterval(refreshWorkbenchBadge, 15000);
setInterval(refreshBriefCard, 60000);
(async () => {
  await loadSummaries();
  await restoreMainSession();
})();
setInterval(refreshCollaboration, 5000);

// ---- 主题：读取持久化值，按钮在 light/dark 间切换 ----
let currentTheme = 'system';
api.getDesktopSettings().then(settings => {
  currentTheme = settings.theme || 'system';
  applyThemeButton();
}).catch(() => {});

function applyThemeButton() {
  if (!themeToggle) return;
  themeToggle.title = currentTheme === 'dark' ? '切换到亮色' : '切换到暗色';
  themeToggle.setAttribute('aria-label', themeToggle.title);
}

themeToggle?.addEventListener('click', async () => {
  const next = currentTheme === 'dark' ? 'light' : 'dark';
  try {
    const result = await api.setTheme(next);
    currentTheme = result.theme;
    applyThemeButton();
  } catch (error) {
    console.error(error);
  }
});
api.onThemeChanged?.(theme => { currentTheme = theme; applyThemeButton(); });

// ---- 流式进度：主进程把 agent 事件推过来 ----
api.onChatStatus(event => {
  const next = describeStatus(event);
  if (next) setChatStatus(next[0], next[1]);
});

// 协作总结若新建了会话，把它记为当前会话，下次启动可恢复。
api.onChatSession?.(id => {
  if (id) {
    localStorage.setItem(SESSION_KEY, id);
    loadSummaries();
  }
});
