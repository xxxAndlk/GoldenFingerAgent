'use strict';

// 工作轨迹的纯函数：错误清洗、类型标签与事件过滤。
// 同时供渲染进程（<script> 挂到 window.ActivityFormat）和 node 测试 require。
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.ActivityFormat = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  const TYPE_META = {
    content: { label: '正文', tone: 'blue' },
    action: { label: '动作', tone: 'green' },
    submission: { label: '提交', tone: 'orange' },
    window: { label: '窗口', tone: 'gray' },
    shortcut: { label: '快捷键', tone: 'orange' },
    connector: { label: '连接', tone: 'green' },
  };

  function typeMeta(type) {
    return TYPE_META[type] || { label: '记录', tone: 'gray' };
  }

  // 低价值事件：没有正文/标题，或只是「应用名 = 标题 = 详情」的空窗口占位。
  function isUsefulEvent(event) {
    if (!event) return false;
    const detail = String(event.detail || '').trim();
    const title = String(event.title || '').trim();
    const app = String(event.app || '').trim();
    if (!detail && !title) return false;
    if (detail && detail === title && title === app) return false;
    return true;
  }

  const POWERSHELL_NOISE = /^\+?\s*(CategoryInfo|FullyQualifiedErrorId|At line:|At char:|At C:\\)/i;

  // 去掉 ANSI 控制字符（\p{Cc}）与 PowerShell 的定位噪声，只留下人能读的一句话。
  function cleanErrorText(raw) {
    return String(raw || '')
      .split(/\r?\n/)
      .map(line => line.replace(/\p{Cc}/gu, '').trim())
      .filter(line => line && !POWERSHELL_NOISE.test(line))
      .join(' ')
      .replace(/\s+/g, ' ')
      .trim();
  }

  // sanitizeError 把 PowerShell/系统异常翻成一句话，原文折叠进详情。
  function sanitizeError(raw) {
    const detail = cleanErrorText(raw).slice(0, 600);
    if (/GoldenFingerInput|TypeNotFound|输入事件|Add-Type|UI ?Automation/i.test(detail)) {
      return {
        title: '输入监听未启动',
        message: '系统组件加载失败，快捷键与提交记录可能缺失；窗口与正文仍在记录。',
        detail,
      };
    }
    if (/观察失败|读取当前窗口|GetForegroundWindow|页面内容|获取窗口/i.test(detail)) {
      return {
        title: '窗口读取失败',
        message: '当前窗口暂时读不到内容，正在按周期重试。',
        detail,
      };
    }
    if (/ENOENT|not found|找不到|不存在/i.test(detail)) {
      return {
        title: '本地组件缺失',
        message: '某次记录缺少必要的本地文件，已跳过这一次。',
        detail,
      };
    }
    return {
      title: '记录遇到问题',
      message: '已自动继续记录；如果反复出现，可在设置里重新开启协作模式。',
      detail,
    };
  }

  function formatClock(ms) {
    return new Date(ms).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
  }

  function summaryExcerpt(text, max = 90) {
    const oneLine = String(text || '').replace(/\s+/g, ' ').trim();
    return oneLine.length > max ? `${oneLine.slice(0, max - 1)}…` : oneLine;
  }

  const APP_LABELS = {
    chrome: 'Chrome',
    msedge: 'Edge',
    firefox: 'Firefox',
    code: 'VS Code',
    windowsterminal: '终端',
    cmd: '命令提示符',
    powershell: 'PowerShell',
    pwsh: 'PowerShell',
    explorer: '资源管理器',
    notepad: '记事本',
    notepadpp: 'Notepad++',
    devenv: 'Visual Studio',
    idea64: 'IntelliJ',
    pycharm64: 'PyCharm',
    wechat: '微信',
    qq: 'QQ',
    dingtalk: '钉钉',
    feishu: '飞书',
    lark: '飞书',
    wps: 'WPS',
    winword: 'Word',
    excel: 'Excel',
    powerpnt: 'PowerPoint',
    obsidian: 'Obsidian',
    notion: 'Notion',
    slack: 'Slack',
    discord: 'Discord',
    spotify: 'Spotify',
    vlc: 'VLC',
    everything: 'Everything',
  };

  // appLabel 把进程名翻成常用软件名；认不出的保持原样，不猜测。
  function appLabel(app) {
    const raw = String(app || '').trim();
    if (!raw) return '未知应用';
    return APP_LABELS[raw.toLowerCase()] || raw;
  }

  const APP_TONES = ['blue', 'green', 'orange', 'purple', 'teal'];
  function appTone(app) {
    const text = appLabel(app);
    let hash = 0;
    for (let i = 0; i < text.length; i += 1) hash = (hash * 31 + text.charCodeAt(i)) % 997;
    return APP_TONES[hash % APP_TONES.length];
  }

  function appInitial(app) {
    const text = appLabel(app);
    if (!text || text === '未知应用') return '·';
    return /[a-z]/i.test(text[0]) ? text[0].toUpperCase() : text[0];
  }

  return {
    typeMeta,
    isUsefulEvent,
    sanitizeError,
    formatClock,
    summaryExcerpt,
    appLabel,
    appTone,
    appInitial,
  };
});
