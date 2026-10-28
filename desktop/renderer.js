'use strict';

const api = window.desktopAssistant;
const orb = document.querySelector('#orb');
const panel = document.querySelector('#panel');
const messages = document.querySelector('#messages');
const form = document.querySelector('#chat-form');
const input = document.querySelector('#chat-input');
const send = document.querySelector('#send');
const toggle = document.querySelector('#collab-toggle');
const collabStatus = document.querySelector('#collab-status');
const summarize = document.querySelector('#summarize');
const connection = document.querySelector('#connection');
const orbDot = document.querySelector('#orb-dot');
let expanded = false;

function renderExpanded(next) {
  expanded = next;
  orb.hidden = next;
  panel.hidden = !next;
  if (!next) orbDot.hidden = true;
  if (next) setTimeout(() => input.focus(), 180);
}

function setExpanded(next) {
  renderExpanded(next);
  api.toggle(next);
}

api.onExpanded(renderExpanded);

function addMessage(text, role = 'assistant') {
  const element = document.createElement('article');
  element.className = `message ${role}`;
  element.textContent = text;
  messages.appendChild(element);
  messages.scrollTop = messages.scrollHeight;
  return element;
}

async function refreshStatus() {
  const status = await api.status();
  connection.textContent = status.connected ? '已连接本机管家' : '管家服务未连接';
  connection.className = `connection ${status.connected ? 'online' : 'offline'}`;
}

function renderCollaboration(status) {
  toggle.checked = status.enabled;
  summarize.disabled = !status.enabled;
  collabStatus.textContent = status.enabled
    ? `已授权 · 待总结采样 ${status.pendingScans} 条`
    : '未授权，不会扫描';
}

orb.addEventListener('click', () => setExpanded(true));
document.querySelector('#collapse').addEventListener('click', () => setExpanded(false));

form.addEventListener('submit', async event => {
  event.preventDefault();
  const text = input.value.trim();
  if (!text) return;
  input.value = '';
  send.disabled = true;
  addMessage(text, 'user');
  const loading = addMessage('正在思考…', 'assistant');
  try {
    const response = await api.chat(text);
    loading.textContent = response.reply || '我没有收到回答。';
  } catch (error) {
    loading.textContent = `发送失败：${error.message}`;
    loading.classList.add('error');
  } finally {
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

toggle.addEventListener('change', async () => {
  toggle.disabled = true;
  try {
    const status = await api.setCollaboration(toggle.checked);
    renderCollaboration(status);
    addMessage(status.enabled
      ? '协作模式已开启。我只读取活动应用和窗口标题，不读取文件内容或键盘输入。'
      : '协作模式已关闭，扫描已停止，尚未总结的内存采样已清空。', 'system');
  } catch (error) {
    toggle.checked = !toggle.checked;
    addMessage(`无法切换协作模式：${error.message}`, 'error');
  } finally {
    toggle.disabled = false;
  }
});

summarize.addEventListener('click', async () => {
  summarize.disabled = true;
  const result = await api.summarizeNow();
  if (result.error) addMessage(result.error, 'error');
  else if (!result.record && result.reply) addMessage(result.reply, 'assistant');
  renderCollaboration(await api.collaborationStatus());
  summarize.disabled = false;
});

api.onSummary(summary => {
  if (summary.error) addMessage(summary.error, 'error');
  if (summary.reply) {
    addMessage(summary.reply, 'assistant');
    if (!expanded) orbDot.hidden = false;
  }
});

setInterval(async () => {
  if (expanded) renderCollaboration(await api.collaborationStatus());
}, 5000);

refreshStatus();
api.collaborationStatus().then(renderCollaboration);
