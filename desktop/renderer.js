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
const historyClearDate = document.querySelector('#history-clear-date');
const attachButton = document.querySelector('#attach');
const attachInput = document.querySelector('#attach-input');
const attachmentStrip = document.querySelector('#attachments');
const UPLOAD_BASE = 'http://127.0.0.1:8080';
const SESSION_KEY = 'gfa.chatSessionId';
const WELCOME = '我是金手指管家，可以对话、记录和调度其他 Agent。右上角是设置与 Agent 管理，顶栏开关控制协作模式。';
let expanded = false;
let historySessions = [];
let pendingAttachments = [];

// ---- 对话状态：思考中 / 调用工具 / 等待 Agent ----
const TOOL_LABELS = {
  web_search: '正在联网检索',
  web_fetch: '正在读取网页',
  read: '正在读取文件',
  write: '正在写入文件',
  bash: '正在执行命令',
  delegate_agent: '正在等待 Agent 完成',
  list_agents: '正在扫描本机 Agent',
  open_agent: '正在唤起 Agent',
  memory_search: '正在检索长期记忆',
  remember: '正在写入长期记忆'
};
const WAITING_TOOLS = new Set(['delegate_agent', 'open_agent']);

function setChatStatus(state, text) {
  setOrbBusy(Boolean(state));
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

function addMessage(text, role = 'assistant', attachments = []) {
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
  messages.appendChild(element);
  messages.scrollTop = messages.scrollHeight;
  return element;
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
      addMessage('最多同时添加 6 个附件', 'system');
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
      addMessage(`附件上传失败：${error.message}`, 'error');
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
  if (summary.error) addMessage(summary.error, 'error');
  if (summary.reply) {
    addMessage(summary.reply, 'assistant');
    if (!expanded) orbDot.hidden = false;
  }
});
api.onAgentResult(({ name, result }) => {
  addMessage(`**${name} 执行结果**\n\n${result.output || '任务已提交。'}`, 'assistant');
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

function renderConversation(list) {
  messages.replaceChildren();
  const items = (list || []).filter(item => item.role === 'user' || item.role === 'assistant');
  if (!items.length) {
    addMessage(WELCOME, 'assistant');
    return;
  }
  for (const item of items) addMessage(item.content || '', item.role);
}

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
    const list = await api.sessionMessages(id);
    renderConversation(list);
    localStorage.setItem(SESSION_KEY, id);
    closeHistory();
    if (!expanded) setExpanded(true);
    input.focus();
  } catch (error) {
    setHistoryMessage(`打开失败：${error.message}`);
  }
}

async function startNewChat() {
  await api.useSession('');
  localStorage.removeItem(SESSION_KEY);
  renderConversation([]);
  closeHistory();
  input.focus();
}

async function restoreLastSession() {
  const saved = localStorage.getItem(SESSION_KEY);
  if (!saved) return;
  try {
    await api.useSession(saved);
    const list = await api.sessionMessages(saved);
    renderConversation(list);
  } catch (error) {
    localStorage.removeItem(SESSION_KEY);
    await api.useSession('');
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
document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && !historyPanel.hidden) closeHistory();
});

orb.addEventListener('click', () => setExpanded(true));
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
  addMessage(text || '（附件）', 'user', files);
  const loading = addMessage('正在思考…', 'assistant');
  setChatStatus('thinking', '正在思考…');
  try {
    const response = await api.chat(text, files);
    if (response.session_id) localStorage.setItem(SESSION_KEY, response.session_id);
    pendingAttachments = [];
    renderAttachments();
    loading.innerHTML = window.MarkdownLite.render(response.reply || '我没有收到回答。');
  } catch (error) {
    loading.textContent = `发送失败：${error.message}`;
    loading.classList.add('error');
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
restoreLastSession();
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
  if (id) localStorage.setItem(SESSION_KEY, id);
});
