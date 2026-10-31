'use strict';

const agentsView = document.querySelector('#agents-view');
const settingsView = document.querySelector('#settings-view');
const refreshButton = document.querySelector('#agents-refresh');

// showView 在「Agent 调度 / 设置」之间切换。
function showView(name) {
  const agents = name !== 'settings';
  agentsView.hidden = !agents;
  settingsView.hidden = agents;
  refreshButton.hidden = !agents;
  for (const item of document.querySelectorAll('[data-view]')) {
    item.classList.toggle('active', item.dataset.view === (agents ? 'agents' : 'settings'));
  }
}

for (const button of document.querySelectorAll('[data-view]')) {
  button.addEventListener('click', () => showView(button.dataset.view));
}

// 主窗口顶栏的「设置」按钮直接落到设置页。
if (window.desktopAssistant && window.desktopAssistant.onSettingsOpen) {
  window.desktopAssistant.onSettingsOpen(() => showView('settings'));
}
