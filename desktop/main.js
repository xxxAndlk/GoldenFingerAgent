'use strict';

const path = require('node:path');
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
const { app, BrowserWindow, ipcMain, screen, Notification, Tray, Menu, nativeImage, dialog, shell, nativeTheme } = require('electron');
const { AssistantApi } = require('./assistant-api');
const { CollaborationMode } = require('./collaboration');
const { readActiveWindow } = require('./active-window');
const { ActivityTrail } = require('./activity-trail');
const { wantsActivitySummary } = require('./activity-summary');
const { captureWindow } = require('./window-capture');
const { readDesktopContent } = require('./desktop-content');
const { readGitEvidence } = require('./git-connector');
const { ServerManager } = require('./server-manager');
const { ScreenpipeSource } = require('./screenpipe-source');
const { ScreenpipeShadow } = require('./screenpipe-shadow');
const {
  clampBounds, snapBounds, dockEdge, nearestEdge, overflowEdge, edgeTabBounds, anchorExpandedBounds,
} = require('./window-dock');

const COLLAPSED_SIZE = { width: 76, height: 76 };
const EXPANDED_SIZE = { width: 460, height: 680 };
const ACTIVITY_SIZE = { width: 340, height: 340 };
const ACTIVITY_TAB = { along: 46, cross: 24 };
const ACTIVITY_HANDLE = { width: 32, height: 46, gap: 2 };
const SNAP_DELAY = 220;
const DEFAULT_ACTIVITY_OPACITY = 76;
// 窗口图标与托盘图标同源，任务栏、Alt+Tab、托盘里看到的都是同一个应用图标。
const APP_ICON = path.join(__dirname, 'app-icon.ico');
// Windows 托盘与通知依赖 AppUserModelID；固定值保证升级后不丢图标。
const APP_USER_MODEL_ID = 'com.goldenfinger.butler';

// --no-sandbox（或某些沙箱启动）会让 Electron 以 Low 完整性运行；
// Windows 会拒绝低完整性进程注册托盘图标，此时只能提示用户正常启动。
function detectLowIntegrity() {
  if (process.platform !== 'win32') return false;
  try {
    const whoami = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'whoami.exe');
    const output = execFileSync(whoami, ['/groups'], { encoding: 'utf8', windowsHide: true, timeout: 4000 });
    return output.includes('S-1-16-4096');
  } catch {
    return false;
  }
}
const LOW_INTEGRITY = detectLowIntegrity();
if (LOW_INTEGRITY) console.warn('当前进程为低完整性（常见于 --no-sandbox 启动），系统托盘不可用。');

let widget = null;
let activityWindow = null;
let activityHandleWindow = null;
let activityHandleSide = 'left';
let activityHandleTimer = null;
let agentWindow = null;
let ccswitchWindow = null;
let tray = null;
let trayBusy = false;
let busyCount = 0;
let trayIconCache = new Map();
let expanded = false;
let widgetDrag = null;
let activityVisible = true;
let activityCollapsed = false;
let activityExpandedBounds = null;
let activityDock = null;
let widgetDock = null;
let quitting = false;
let collaboration = null;
let activityTrail = null;
let workspace = '';
let chatSessionId = '';
let api = null;
let serverManager = null;
let screenpipeShadow = null;

const hasSingleInstanceLock = app.requestSingleInstanceLock();
if (!hasSingleInstanceLock) app.quit();
else app.on('second-instance', () => showWidget());
if (process.platform === 'win32') app.setAppUserModelId(APP_USER_MODEL_ID);

// 长期记忆由 Go 服务内置实现承担（internal/memory/native.go），
// 桌面端不再拉起任何 Python/Graphiti 进程。
async function ensureServer() {
  if (!serverManager) throw new Error('本机服务尚未初始化');
  // 长期记忆已内置在 Go 服务里（internal/memory/native.go），
  // 不再有需要单独启动的 Python 进程。
  return serverManager.ensure();
}

function settingsPath() {
  return path.join(app.getPath('userData'), 'desktop-settings.json');
}

function readSettings() {
  try {
    return JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
  } catch {
    return { collaborationEnabled: false };
  }
}

function saveSettings(settings) {
  fs.mkdirSync(path.dirname(settingsPath()), { recursive: true });
  fs.writeFileSync(settingsPath(), JSON.stringify({ ...readSettings(), ...settings }, null, 2));
}

function validActivityOpacity(value) {
  const opacity = Number(value);
  if (!Number.isFinite(opacity)) throw new Error('透明度必须是数字');
  return Math.round(Math.max(20, Math.min(95, opacity)));
}

function validBounds(value) {
  if (!value || typeof value !== 'object') return null;
  const { x, y, width, height } = value;
  if (![x, y, width, height].every(Number.isFinite)) return null;
  if (width < 40 || height < 40) return null;
  return { x: Math.round(x), y: Math.round(y), width: Math.round(width), height: Math.round(height) };
}

// normalizeTheme 把界面传入的主题归一化为 system|light|dark。
function normalizeTheme(value) {
  const v = String(value || '').toLowerCase();
  return v === 'light' || v === 'dark' ? v : 'system';
}

// applyTheme 通过 nativeTheme 切换整棵窗口树的外观，
// 三个窗口都靠 prefers-color-scheme 令牌自动跟随，无需逐窗改样式。
function applyTheme(value) {
  const theme = normalizeTheme(value);
  nativeTheme.themeSource = theme;
  return theme;
}

function broadcast(channel, payload) {
  for (const window of [widget, activityWindow, agentWindow, ccswitchWindow]) {
    if (window && !window.isDestroyed()) window.webContents.send(channel, payload);
  }
}

// watchDock 在拖动停顿后吸附屏幕边缘；setBounds 触发的 move 用 suppress 忽略。
// 悬浮球用指针坐标手动拖拽：Electron 的 app-region drag 会吞掉点击，
// 无法同时做到“点球展开、按住球拖动”，所以这里用 pointer 事件区分点击和拖动。
function beginWidgetDrag(x, y) {
  if (!widget || widget.isDestroyed() || expanded) return false;
  widgetDrag = { cursorX: Number(x) || 0, cursorY: Number(y) || 0, bounds: widget.getBounds() };
  return true;
}

function moveWidgetDrag(x, y) {
  if (!widgetDrag || !widget || widget.isDestroyed()) return;
  const dx = (Number(x) || 0) - widgetDrag.cursorX;
  const dy = (Number(y) || 0) - widgetDrag.cursorY;
  const next = {
    ...widgetDrag.bounds,
    x: widgetDrag.bounds.x + Math.round(dx),
    y: widgetDrag.bounds.y + Math.round(dy),
  };
  const area = screen.getDisplayMatching(next).workArea;
  const clamped = clampBounds(next, area);
  widget.setPosition(clamped.x, clamped.y);
}

function endWidgetDrag() {
  if (!widgetDrag) return;
  widgetDrag = null;
  if (widget && !widget.isDestroyed()) saveSettings({ widgetBounds: widget.getBounds() });
}

// watchDock 在拖动停顿后吸附屏幕边缘，仅用于工作轨迹窗。
function watchDock(win, onDock, onOverflow = null) {
  let timer = null;
  let suppress = false;
  const schedule = () => {
    if (suppress) return;
    clearTimeout(timer);
    timer = setTimeout(() => {
      if (win.isDestroyed()) return;
      const bounds = win.getBounds();
      const area = screen.getDisplayMatching(bounds).workArea;
      const overflow = overflowEdge(bounds, area);
      if (overflow && onOverflow) {
        onOverflow(overflow);
        return;
      }
      const position = snapBounds(bounds, area);
      const edge = dockEdge({ ...bounds, ...position }, area, 2);
      if (position.x !== bounds.x || position.y !== bounds.y) {
        suppress = true;
        win.setBounds({ ...bounds, ...position }, true);
        setTimeout(() => { suppress = false; }, 180);
      }
      onDock(edge, { ...bounds, ...position });
    }, SNAP_DELAY);
  };
  win.on('move', schedule);
  win.on('closed', () => clearTimeout(timer));
}

function watchPosition(win, onMove) {
  let timer = null;
  const schedule = () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      if (!win.isDestroyed()) onMove(win.getBounds());
    }, 180);
  };
  win.on('move', schedule);
  win.on('closed', () => clearTimeout(timer));
}

function collapsedPositionFor(panelBounds, area) {
  const size = COLLAPSED_SIZE;
  const edge = dockEdge(panelBounds, area, 2);
  let x = panelBounds.x + panelBounds.width - size.width;
  let y = panelBounds.y + Math.round((panelBounds.height - size.height) / 2);
  if (edge === 'left') x = area.x;
  else if (edge === 'right') x = area.x + area.width - size.width;
  if (edge === 'top') y = area.y;
  else if (edge === 'bottom') y = area.y + area.height - size.height;
  return { ...size, x: Math.max(area.x, Math.min(x, area.x + area.width - size.width)), y: Math.max(area.y, Math.min(y, area.y + area.height - size.height)) };
}

function setExpanded(next) {
  expanded = Boolean(next);
  if (!widget || widget.isDestroyed()) return;
  const current = widget.getBounds();
  const area = screen.getDisplayMatching(current).workArea;
  const bounds = expanded
    ? anchorExpandedBounds(current, EXPANDED_SIZE, area, widgetDock)
    : collapsedPositionFor(current, area);
  if (!expanded) {
    widgetDock = '';
    saveSettings({ widgetBounds: bounds });
  }
  widget.setBounds(bounds, true);
  widget.webContents.send('widget:expanded', expanded);
  widget.webContents.send('widget:dock', widgetDock);
}

// 工作轨迹独立于协作模式：窗口展示着就记录，隐藏且未开协作时停掉。
async function syncActivityRecording() {
  if (!activityTrail) return;
  const shouldRun = activityVisible || Boolean(collaboration && collaboration.enabled);
  if (shouldRun && !activityTrail.enabled) {
    try {
      await activityTrail.start();
    } catch (error) {
      console.error(`工作轨迹启动失败：${error.message}`);
    }
  } else if (!shouldRun && activityTrail.enabled) {
    activityTrail.stop();
  }
}

function hideActivityHandle() {
  if (activityHandleWindow && !activityHandleWindow.isDestroyed()) activityHandleWindow.hide();
}

function scheduleActivityHandleSync() {
  clearTimeout(activityHandleTimer);
  activityHandleTimer = setTimeout(() => {
    activityHandleTimer = null;
    syncActivityHandle();
  }, 30);
}

function ensureActivityHandleWindow() {
  if (activityHandleWindow && !activityHandleWindow.isDestroyed()) return activityHandleWindow;
  activityHandleWindow = new BrowserWindow({
    width: ACTIVITY_HANDLE.width,
    height: ACTIVITY_HANDLE.height,
    show: false,
    frame: false,
    transparent: true,
    resizable: false,
    movable: false,
    minimizable: false,
    maximizable: false,
    fullscreenable: false,
    alwaysOnTop: true,
    skipTaskbar: true,
    focusable: true,
    hasShadow: false,
    backgroundColor: '#00000000',
    webPreferences: windowPreferences(),
  });
  activityHandleWindow.setAlwaysOnTop(true, 'floating');
  activityHandleWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  activityHandleWindow.webContents.on('did-finish-load', () => {
    if (!activityHandleWindow || activityHandleWindow.isDestroyed()) return;
    activityHandleWindow.webContents.send('activity:handle-side', activityHandleSide);
    syncActivityHandle();
  });
  activityHandleWindow.on('closed', () => { activityHandleWindow = null; });
  activityHandleWindow.loadFile(path.join(__dirname, 'activity-handle.html'));
  return activityHandleWindow;
}

// 收回按钮放在卡片外侧的独立小窗口里，避免主窗口留出透明空列。
function syncActivityHandle() {
  if (!activityWindow || activityWindow.isDestroyed() || !activityVisible || activityCollapsed || !activityDock) {
    hideActivityHandle();
    return;
  }
  const bounds = activityWindow.getBounds();
  const area = screen.getDisplayMatching(bounds).workArea;
  const width = Math.min(ACTIVITY_HANDLE.width, area.width);
  const height = Math.min(ACTIVITY_HANDLE.height, area.height);
  let side = 'left';
  let x = bounds.x - width - ACTIVITY_HANDLE.gap;
  if (x < area.x) {
    side = 'right';
    x = bounds.x + bounds.width + ACTIVITY_HANDLE.gap;
  }
  x = Math.min(Math.max(x, area.x), area.x + area.width - width);
  const y = Math.min(Math.max(
    bounds.y + Math.round((bounds.height - height) / 2),
    area.y,
  ), area.y + area.height - height);
  const handle = ensureActivityHandleWindow();
  if (handle.webContents.isLoading()) {
    handle.hide();
    return;
  }
  activityHandleSide = side;
  handle.setBounds({ x, y, width, height });
  handle.webContents.send('activity:handle-side', side);
  handle.showInactive();
}

function setActivityVisible(next) {
  activityVisible = Boolean(next);
  if (!activityWindow || activityWindow.isDestroyed()) createActivityWindow();
  if (activityVisible) activityWindow.showInactive();
  else activityWindow.hide();
  syncActivityHandle();
  saveSettings({ activityVisible });
  broadcast('activity:visible', activityVisible);
  refreshTrayMenu();
  void syncActivityRecording();
  return activityVisible;
}

function setActivityCollapsed(next, forcedEdge = '') {
  if (!activityWindow || activityWindow.isDestroyed()) createActivityWindow();
  const nextState = Boolean(next);
  if (nextState === activityCollapsed) {
    if (activityVisible) activityWindow.showInactive();
    syncActivityHandle();
    return activityCollapsed;
  }
  const current = activityWindow.getBounds();
  const area = screen.getDisplayMatching(current).workArea;
  if (nextState) {
    activityExpandedBounds = current;
    activityDock = forcedEdge || dockEdge(current, area, 2) || nearestEdge(current, area);
    activityWindow.setBounds(edgeTabBounds(current, area, activityDock, ACTIVITY_TAB), true);
    saveSettings({ activityExpandedBounds, activityCollapsed: true, activityDock });
  } else {
    const target = clampBounds(activityExpandedBounds || {
      ...ACTIVITY_SIZE,
      x: area.x + area.width - ACTIVITY_SIZE.width - 18,
      y: area.y + 20,
    }, area);
    activityWindow.setBounds(target, true);
    activityDock = dockEdge(target, area, 2);
    saveSettings({ activityExpandedBounds: target, activityCollapsed: false });
  }
  activityCollapsed = nextState;
  broadcast('activity:collapsed', { collapsed: activityCollapsed, edge: activityDock });
  if (activityVisible) activityWindow.showInactive();
  syncActivityHandle();
  refreshTrayMenu();
  return activityCollapsed;
}

function showWidget() {
  if (!widget || widget.isDestroyed()) createWidget();
  if (!activityWindow || activityWindow.isDestroyed()) createActivityWindow();
  widget.show();
  setExpanded(true);
  widget.focus();
  if (process.platform === 'win32') app.focus({ steal: true });
}

function loadTrayIcon(variant) {
  const key = variant || 'idle';
  if (trayIconCache.has(key)) return trayIconCache.get(key);
  const base = key === 'busy' ? 'tray-icon-busy' : 'tray-icon';
  const files = process.platform === 'win32' ? [`${base}.ico`, `${base}.png`] : [`${base}.png`];
  for (const file of files) {
    const image = nativeImage.createFromPath(path.join(__dirname, file));
    if (!image.isEmpty()) {
      trayIconCache.set(key, image);
      return image;
    }
  }
  return null;
}

// setTrayState 切换托盘图标与提示：执行中显示忙碌图标，停下来恢复「运行中」。
function setTrayState(busy, detail = '') {
  trayBusy = Boolean(busy);
  if (!tray || tray.isDestroyed()) return;
  const icon = loadTrayIcon(trayBusy ? 'busy' : 'idle');
  if (icon) tray.setImage(icon);
  tray.setToolTip(trayBusy ? `金手指管家 · ${detail || '正在执行'}` : '金手指管家 · 运行中');
}

// 对话与总结可能同时进行，用计数避免先结束的一方把托盘错误地切回空闲。
function beginBusy(detail) {
  busyCount += 1;
  setTrayState(true, detail);
}

function endBusy() {
  busyCount = Math.max(0, busyCount - 1);
  if (busyCount === 0) setTrayState(false);
}

function trayLabelForStatus(status) {
  if (!status) return '正在思考';
  if (status.kind === 'tool') return `正在执行 ${status.name || '工具'}`;
  if (status.kind === 'tool_done') return '正在整理结果';
  if (status.kind === 'clarify') return '等待你确认';
  return '正在思考';
}

function buildTrayMenu() {
  return Menu.buildFromTemplate([
    { label: '显示 / 展开对话', click: () => showWidget() },
    { label: '收起对话面板', click: () => setExpanded(false) },
    {
      label: !activityVisible ? '显示工作轨迹' : activityCollapsed ? '展开工作轨迹' : '隐藏工作轨迹',
      click: () => {
        if (!activityVisible) {
          setActivityVisible(true);
          if (activityCollapsed) setActivityCollapsed(false);
        } else if (activityCollapsed) {
          setActivityCollapsed(false);
        } else {
          setActivityVisible(false);
        }
      },
    },
    { label: '立即总结一次', enabled: Boolean(collaboration && collaboration.enabled), click: () => runSummarize() },
    { type: 'separator' },
    { label: 'Agent 与设置', click: () => openAuxWindow('agents') },
    { label: 'CC Switch 代理与额度', click: () => openAuxWindow('ccswitch') },
    { type: 'separator' },
    { label: '退出金手指管家', click: () => app.quit() },
  ]);
}

function refreshTrayMenu() {
  if (!tray || tray.isDestroyed()) return;
  tray.setContextMenu(buildTrayMenu());
}

// Windows 11 默认把新托盘图标放进「^」隐藏图标区；若系统里已有本程序的
// NotifyIconSettings 记录，把 isPromoted 置 1，让它回到任务栏。
function promoteTrayIcon() {
  if (process.platform !== 'win32' || LOW_INTEGRITY) return false;
  const run = args => execFileSync('reg.exe', args, { encoding: 'utf8', windowsHide: true, timeout: 4000 });
  const root = 'HKCU\\Control Panel\\NotifyIconSettings';
  try {
    const listing = run(['query', root, '/s']);
    let current = '';
    const keys = [];
    for (const raw of listing.split(/\r?\n/)) {
      const line = raw.trim();
      if (!line) continue;
      if (/^HKEY_/i.test(line)) { current = line; continue; }
      const match = line.match(/^ExecutablePath\s+REG_SZ\s+(.*)$/i);
      if (match && current && match[1].trim().toLowerCase() === process.execPath.toLowerCase()) {
        keys.push(current);
      }
    }
    let promoted = false;
    for (const key of keys) {
      let already = false;
      try { already = /0x1\b/i.test(run(['query', key, '/v', 'isPromoted'])); } catch { /* 值不存在 */ }
      if (already) continue;
      try {
        run(['add', key, '/v', 'isPromoted', '/t', 'REG_DWORD', '/d', '1', '/f']);
        promoted = true;
      } catch (error) {
        console.error(`托盘图标提升失败：${error.message}`);
      }
    }
    return promoted;
  } catch (error) {
    console.error(`读取托盘设置失败：${error.message}`);
    return false;
  }
}

function trayVisible() {
  if (!tray || tray.isDestroyed()) return false;
  const bounds = tray.getBounds();
  return bounds.width > 0 && bounds.height > 0;
}

// 托盘图标被收进隐藏区时给一次明确提示（Windows 11 无法由程序强制显示）。
function announceTrayHidden() {
  if (process.platform !== 'win32' || LOW_INTEGRITY) return;
  if (trayVisible()) return;
  const settings = readSettings();
  if (settings.trayOverflowHintShown === true) return;
  if (!Notification.isSupported()) return;
  new Notification({
    title: '金手指管家 · 托盘图标在隐藏图标区',
    body: '点击任务栏的「^」找到金色「金」图标，把它拖到任务栏即可固定。点击图标会展开主对话；也可以直接点悬浮球。',
  }).show();
  saveSettings({ trayOverflowHintShown: true });
}

function createTray() {
  const icon = loadTrayIcon('idle');
  if (!icon) throw new Error('托盘图标加载失败');
  tray = new Tray(icon);
  tray.setToolTip('金手指管家 · 运行中');
  refreshTrayMenu();
  tray.on('click', () => showWidget());
  tray.on('double-click', () => showWidget());
}

// 首次启动用一条气泡把「已在托盘运行」说清楚，之后不再打扰。
function announceTrayOnce() {
  const settings = readSettings();
  if (settings.trayHintShown === true) return;
  if (tray && !tray.isDestroyed()) {
    tray.displayBalloon({
      title: '金手指管家已在运行',
      content: '点托盘图标展开对话；右键可以打开设置、立即总结或退出。',
    });
  }
  saveSettings({ trayHintShown: true });
}

// runSummarize 供托盘与轨迹窗口共用：执行一次立即总结，并广播结果。
async function runSummarize() {
  if (!collaboration) return { error: '协作模式尚未初始化' };
  beginBusy('正在总结');
  try {
    return await collaboration.summarizeNow();
  } finally {
    endBusy();
    refreshTrayMenu();
  }
}

function windowPreferences() {
  return {
    preload: path.join(__dirname, 'preload.js'),
    contextIsolation: true,
    nodeIntegration: false,
    sandbox: true,
  };
}

function allowSafeExternalLinks(window) {
  window.webContents.setWindowOpenHandler(({ url }) => {
    try {
      const target = new URL(url);
      if (target.protocol === 'https:' || target.protocol === 'http:') shell.openExternal(target.href);
    } catch { /* 忽略无效链接，不在应用内打开。 */ }
    return { action: 'deny' };
  });
}

function openAuxWindow(kind) {
  const configs = {
    agents: { file: 'agents.html', title: 'Agent 与设置', width: 900, height: 780, minWidth: 620, minHeight: 560 },
    ccswitch: { file: 'ccswitch.html', title: 'CC Switch 代理与额度', width: 720, height: 700, minWidth: 520, minHeight: 480 },
  };
  const config = configs[kind];
  if (!config) throw new Error('未知窗口类型');
  let target = kind === 'agents' ? agentWindow : ccswitchWindow;
  if (target && !target.isDestroyed()) {
    target.show();
    target.focus();
    return target;
  }
  target = new BrowserWindow({
    width: config.width,
    height: config.height,
    minWidth: config.minWidth,
    minHeight: config.minHeight,
    title: config.title,
    icon: APP_ICON,
    show: false,
    autoHideMenuBar: true,
    backgroundColor: '#111715',
    webPreferences: windowPreferences(),
  });
  if (kind === 'agents') agentWindow = target;
  else ccswitchWindow = target;
  allowSafeExternalLinks(target);
  target.once('ready-to-show', () => target.show());
  target.on('closed', () => {
    if (kind === 'agents') agentWindow = null;
    else ccswitchWindow = null;
  });
  target.loadFile(path.join(__dirname, config.file));
  return target;
}

function createWidget() {
  const settings = readSettings();
  const area = screen.getPrimaryDisplay().workArea;
  const size = COLLAPSED_SIZE;
  const saved = validBounds(settings.widgetBounds);
  const start = clampBounds(saved || {
    ...size,
    x: area.x + area.width - size.width - 28,
    y: area.y + area.height - size.height - 28,
  }, area);
  widgetDock = '';
  widget = new BrowserWindow({
    ...start,
    frame: false,
    transparent: true,
    resizable: false,
    maximizable: false,
    fullscreenable: false,
    alwaysOnTop: true,
    skipTaskbar: true,
    icon: APP_ICON,
    backgroundColor: '#00000000',
    webPreferences: windowPreferences(),
  });
  widget.setAlwaysOnTop(true, 'floating');
  allowSafeExternalLinks(widget);
  watchPosition(widget, bounds => {
    if (!expanded) saveSettings({ widgetBounds: bounds });
  });
  widget.webContents.on('did-finish-load', () => {
    widget.webContents.send('widget:dock', '');
    widget.webContents.send('widget:expanded', expanded);
  });
  widget.on('close', event => {
    if (quitting) return;
    event.preventDefault();
    setExpanded(false);
  });
  widget.loadFile(path.join(__dirname, 'index.html'));
}

function createActivityWindow() {
  const settings = readSettings();
  const area = screen.getPrimaryDisplay().workArea;
  const size = {
    width: Math.min(ACTIVITY_SIZE.width, area.width - 32),
    height: Math.min(ACTIVITY_SIZE.height, area.height - 40),
  };
  const raw = validBounds(settings.activityExpandedBounds) || {
    ...size,
    x: area.x + area.width - size.width - 18,
    y: area.y + 20,
  };
  // 旧版本保存的轨迹窗高度可能更长；按当前上限裁剪，不移动它的位置。
  const saved = clampBounds({
    ...raw,
    width: Math.min(raw.width, size.width),
    height: Math.min(raw.height, size.height),
  }, area);
  activityExpandedBounds = saved;
  saveSettings({ activityExpandedBounds: saved });
  activityCollapsed = settings.activityCollapsed === true;
  activityDock = activityCollapsed
    ? (settings.activityDock || nearestEdge(saved, area))
    : dockEdge(saved, area, 2);
  const bounds = activityCollapsed ? edgeTabBounds(saved, area, activityDock, ACTIVITY_TAB) : saved;
  activityWindow = new BrowserWindow({
    ...bounds,
    show: false,
    frame: false,
    transparent: true,
    resizable: false,
    maximizable: false,
    fullscreenable: false,
    alwaysOnTop: true,
    skipTaskbar: true,
    icon: APP_ICON,
    backgroundColor: '#00000000',
    webPreferences: windowPreferences(),
  });
  activityWindow.setAlwaysOnTop(true, 'floating');
  allowSafeExternalLinks(activityWindow);
  watchDock(activityWindow, (edge, current) => {
    if (activityCollapsed) return;
    activityDock = edge;
    saveSettings({ activityExpandedBounds: current, activityDock: edge });
    if (!activityWindow.isDestroyed()) {
      activityWindow.webContents.send('activity:collapsed', { collapsed: false, edge });
    }
    syncActivityHandle();
  }, edge => setActivityCollapsed(true, edge));
  activityWindow.webContents.on('did-finish-load', () => {
    activityWindow.webContents.send('activity:collapsed', { collapsed: activityCollapsed, edge: activityDock });
    syncActivityHandle();
  });
  activityWindow.on('move', scheduleActivityHandleSync);
  activityWindow.on('show', syncActivityHandle);
  activityWindow.on('hide', hideActivityHandle);
  activityWindow.on('close', event => {
    if (quitting) return;
    event.preventDefault();
    setActivityVisible(false);
  });
  activityWindow.on('closed', hideActivityHandle);
  activityWindow.loadFile(path.join(__dirname, 'activity.html'));
}

app.whenReady().then(async () => {
  if (!hasSingleInstanceLock) return;
  const settings = readSettings();
  applyTheme(settings.theme);
  api = new AssistantApi({
    baseUrl: process.env.GFA_SERVER_URL || 'http://127.0.0.1:8080',
    token: process.env.GFA_TOKEN || '',
    userId: process.env.GFA_USER_ID || '',
  });
  collaboration = new CollaborationMode({
    readActiveWindow,
    api,
    onSummary: summary => {
      broadcast('collaboration:summary', summary);
      if (summary.reply && Notification.isSupported()) {
        new Notification({ title: '金手指 · 协作总结', body: summary.reply.slice(0, 180) }).show();
      }
    },
    onSummarized: (observations, phase = {}) => {
      if (!activityTrail) return;
      const ids = observations.map(item => item.id).filter(Boolean);
      activityTrail.markSummarized(ids, phase.id || '', phase.completedAt || Date.now());
    },
    loadLastSummary: () => (activityTrail ? activityTrail.lastSummary() : ''),
    onPhase: phase => { if (activityTrail) activityTrail.saveSummaryPhase(phase); },
    sessionId: () => chatSessionId,
    onSession: id => {
      chatSessionId = id;
      broadcast('chat:session', id);
    },
  });
  workspace = settings.workspace || '';
  screenpipeShadow = new ScreenpipeShadow({
    source: new ScreenpipeSource({
      baseUrl: process.env.GFA_SCREENPIPE_URL || 'http://127.0.0.1:3030',
      apiKey: process.env.GFA_SCREENPIPE_API_KEY || '',
    }),
    outputDir: path.join(app.getPath('userData'), 'screenpipe-shadow'),
    onError: error => console.error(error),
  });
  activityTrail = new ActivityTrail({
    readActiveWindow,
    readPageContent: options => readDesktopContent(options),
    captureWindow: options => captureWindow(options),
    evidenceDir: path.join(app.getPath('userData'), 'activity-evidence'),
    dataDir: app.getPath('userData'),
    inputMonitorEnabled: !LOW_INTEGRITY,
    onEvent: event => {
      collaboration.addObservation(event);
      screenpipeShadow.addNative(event);
      broadcast('activity:event', event);
    },
    onError: error => broadcast('activity:error', error),
  });
  collaboration.seedPending(activityTrail.pendingObservations());
  collaboration.lastSummary = activityTrail.lastSummary();
  serverManager = new ServerManager({
    appDir: __dirname,
    userData: app.getPath('userData'),
    baseUrl: process.env.GFA_SERVER_URL || 'http://127.0.0.1:8080',
  });
  try { await ensureServer(); } catch (error) { console.error(error.message); }
  try {
    createTray();
    if (!process.argv.includes('--smoke-test')) announceTrayOnce();
  } catch (error) {
    // 托盘失败不能让悬浮球与轨迹窗口一起消失；保留窗口，错误写日志。
    console.error(`托盘初始化失败：${error.message}`);
  }
  createWidget();
  createActivityWindow();
  // 影子模式默认关闭，只能在用户明确配置后读取 Screenpipe；不会启动或安装它。
  if (settings.screenpipeShadowEnabled === true || process.env.GFA_SCREENPIPE_SHADOW === '1') {
    await screenpipeShadow.start();
  }
  if (process.argv.includes('--smoke-test')) {
    widget.webContents.once('did-finish-load', () => {
      const now = Date.now();
      activityTrail.record({ type: 'content', app: 'Chrome', title: '个人管家设计方案', detail: '协作模式应记录页面正文、关键结论和待办事项，而不是只记录鼠标点击。', at: now - 120000 }, false);
      activityTrail.record({ type: 'content', app: 'Code', title: 'main.js - GoldenFingerAgent', detail: '实现独立工作轨迹浮窗，并支持调整背景透明度。', at: now - 60000 }, false);
      setActivityVisible(true);
      setExpanded(true);
      setTimeout(async () => {
        try {
          if (!tray || tray.isDestroyed()) throw new Error('托盘未创建');
          const mainState = await widget.webContents.executeJavaScript(`({
            settings: Boolean(document.querySelector('#model-settings')),
            agents: Boolean(document.querySelector('#agent-panel')),
            chat: Boolean(document.querySelector('#chat-form')),
            collaboration: Boolean(document.querySelector('#main-collab-toggle')),
          })`);
          if (mainState.settings || mainState.agents || !mainState.chat || !mainState.collaboration) throw new Error(`主窗口布局自检失败：${JSON.stringify(mainState)}`);
          setExpanded(false);
          if (!activityWindow || activityWindow.isDestroyed() || !activityWindow.isVisible()) throw new Error('聊天收起后工作轨迹未独立显示');
          setExpanded(true);
          await widget.webContents.executeJavaScript(`document.querySelector('#agents-toggle').click()`);
          await new Promise(resolve => {
            if (!agentWindow.webContents.isLoading()) resolve();
            else agentWindow.webContents.once('did-finish-load', resolve);
          });
          let agentState;
          const agentDeadline = Date.now() + 12_000;
          do {
            await new Promise(resolve => setTimeout(resolve, 250));
            agentState = await agentWindow.webContents.executeJavaScript(`(() => {
              const cards = [...document.querySelectorAll('.agent-card')];
              return {
                count: cards.length,
                status: document.querySelector('#agent-status').textContent,
                desktopOpen: document.querySelectorAll('[data-action="open"]').length,
                directoryPicker: Boolean(document.querySelector('#workdir-choose')),
              };
            })()`);
            if (!agentState.status.startsWith('正在读取')) break;
          } while (Date.now() < agentDeadline);
          if (!agentState.count || !agentState.desktopOpen || !agentState.directoryPicker || agentState.status.includes('失败')) {

            throw new Error(`桌面 Agent 自检失败：${JSON.stringify(agentState)}`);
          }
          const cliState = await agentWindow.webContents.executeJavaScript(`(() => {
            document.querySelector('[data-filter="cli"]').click();
            return {
              count: document.querySelectorAll('.agent-card').length,
              startButtons: document.querySelectorAll('[data-action="open"]').length,
            };
          })()`);
          if (!cliState.count || cliState.startButtons !== cliState.count) throw new Error(`CLI 启动入口自检失败：${JSON.stringify(cliState)}`);
          const serviceState = await agentWindow.webContents.executeJavaScript(`(() => {
            document.querySelector('[data-filter="service"]').click();
            const bma = [...document.querySelectorAll('.agent-card')].find(card => card.textContent.includes('BMA'));
            return {
              count: document.querySelectorAll('.agent-card').length,
              bmaEnabled: Boolean(bma && bma.querySelector('[data-action="delegate"]:not(:disabled)')),
              a2aVisible: !document.querySelector('#a2a-config').hidden,
              desktopOpen: document.querySelectorAll('[data-action="open"]').length,
            };
          })()`);
          if (!serviceState.count || !serviceState.bmaEnabled || !serviceState.a2aVisible || serviceState.desktopOpen) {
            throw new Error(`服务 Agent 自检失败：${JSON.stringify(serviceState)}`);
          }
          const settingsTarget = agentWindow;
          await settingsTarget.webContents.executeJavaScript(`document.querySelector('[data-view="settings"]').click()`);
          await new Promise(resolve => setTimeout(resolve, 700));
          const settingsState = await settingsTarget.webContents.executeJavaScript(`({
            form: Boolean(document.querySelector('#settings-form')),
            visible: !document.querySelector('#settings-view').hidden,
            agentsHidden: document.querySelector('#agents-view').hidden,
            protocol: document.querySelector('#protocol').value,
            status: document.querySelector('#settings-status').textContent,
            relayCard: Boolean(document.querySelector('#relay-pair-code')),
          })`);
          if (!settingsState.form || !settingsState.visible || !settingsState.agentsHidden || settingsState.protocol !== 'responses' || settingsState.status.includes('失败') || !settingsState.relayCard) {
            throw new Error(`合并设置页自检失败：${JSON.stringify(settingsState)}`);
          }
          const ccswitchTarget = openAuxWindow('ccswitch');
          await new Promise(resolve => {
            if (!ccswitchTarget.webContents.isLoading()) resolve();
            else ccswitchTarget.webContents.once('did-finish-load', resolve);
          });
          await new Promise(resolve => setTimeout(resolve, 700));
          const output = path.join(process.cwd(), 'artifacts');
          fs.mkdirSync(output, { recursive: true });
          await captureWindow({ title: '金手指管家', outputDir: output });
          fs.writeFileSync(path.join(output, 'desktop-panel.png'), (await widget.webContents.capturePage()).toPNG());
          fs.writeFileSync(path.join(output, 'activity-panel.png'), (await activityWindow.webContents.capturePage()).toPNG());
          fs.writeFileSync(path.join(output, 'desktop-agents.png'), (await agentWindow.webContents.capturePage()).toPNG());
          fs.writeFileSync(path.join(output, 'desktop-settings.png'), (await settingsTarget.webContents.capturePage()).toPNG());
          fs.writeFileSync(path.join(output, 'desktop-ccswitch.png'), (await ccswitchTarget.webContents.capturePage()).toPNG());
          console.log(`DESKTOP_SMOKE_OK tray=created windows=independent agents=${agentState.count} bma=callable directory=present protocol=responses activity=independent-after-collapse capture=ok`);
          if (serverManager) serverManager.stop();
          app.exit(0);
        } catch (error) {
          console.error(error);
          app.exit(2);
        }
      }, 1000);
    });
    setTimeout(() => app.exit(2), 25_000);
  } else {
    setActivityVisible(settings.activityVisible !== false);
    if (settings.collaborationEnabled) await collaboration.setEnabled(true);
    // 轨迹窗口开着就记录，不再要求先开协作模式。
    await syncActivityRecording();
    refreshTrayMenu();
    setTimeout(() => {
      if (LOW_INTEGRITY) {
        console.log('托盘跳过：进程为低完整性');
        return;
      }
      console.log(`托盘状态：${tray && !tray.isDestroyed() ? JSON.stringify(tray.getBounds()) : '未创建'}`);
      // Win11 新托盘图标默认进隐藏区；若系统已有记录则提升回任务栏。
      const promoted = promoteTrayIcon();
      if (promoted && !trayVisible()) {
        try {
          if (tray && !tray.isDestroyed()) tray.destroy();
          tray = null;
          createTray();
        } catch (error) {
          console.error(`重建托盘失败：${error.message}`);
        }
      }
      announceTrayHidden();
    }, 1800);
  }

  app.on('activate', () => showWidget());
});

app.on('before-quit', () => {
  quitting = true;
  clearTimeout(activityHandleTimer);
  if (activityHandleWindow && !activityHandleWindow.isDestroyed()) activityHandleWindow.destroy();
  if (activityTrail) activityTrail.stop();
  if (screenpipeShadow) screenpipeShadow.stop();
  if (serverManager) serverManager.stop();
});

ipcMain.handle('widget:set-expanded', (_event, next) => setExpanded(next));
ipcMain.handle('widget:drag-start', (_event, x, y) => beginWidgetDrag(x, y));
ipcMain.handle('widget:drag-move', (_event, x, y) => { moveWidgetDrag(x, y); return true; });
ipcMain.handle('widget:drag-end', () => { endWidgetDrag(); return true; });
ipcMain.handle('ui:open-window', (_event, kind) => {
  openAuxWindow(String(kind || ''));
  return true;
});
// 顶栏「设置」：打开管理窗口并直接切到设置页。
ipcMain.handle('ui:open-settings', async () => {
  const target = openAuxWindow('agents');
  const send = () => target.webContents.send('settings:open');
  if (target.webContents.isLoading()) target.webContents.once('did-finish-load', send);
  else send();
  return true;
});
ipcMain.handle('assistant:status', async () => {
  try {
    await ensureServer();
    await api.status();
    return { connected: true };
  } catch (error) {
    return { connected: false, error: error.message };
  }
});
ipcMain.handle('assistant:sessions', async (_event, date) => {
  await ensureServer();
  return api.listSessions(String(date || ''));
});
ipcMain.handle('assistant:session-messages', async (_event, id, before, limit) => {
  await ensureServer();
  return api.sessionMessages(String(id || ''), String(before || ''), Number(limit) || 50);
});
ipcMain.handle('assistant:use-session', (_event, id) => {
  chatSessionId = String(id || '');
  return chatSessionId;
});
ipcMain.handle('assistant:chat', async (_event, text, attachments) => {
  const cleanText = String(text || '').trim().slice(0, 8000);
  const files = Array.isArray(attachments) ? attachments.filter(item => item && item.url).slice(0, 6) : [];
  if (!cleanText && !files.length) throw new Error('请输入内容');
  await ensureServer();
  beginBusy('正在思考');
  try {
    // 走流式接口：把「思考 / 调用工具 / 澄清」实时推给面板，
    // 最终结果仍按原结构返回，渲染逻辑不变。
    // 只有明确询问工作/今天进展时才调用独立总结子代理；
    // 主对话只拿压缩后的短总结，不背当天原始轨迹和大量阶段总结。
    let workContext = '';
    if (wantsActivitySummary(cleanText) && activityTrail) {
      try {
        const rawContext = activityTrail.chatContext({ maxChars: 16000 });
        if (rawContext) {
          const summary = await api.summarizeActivity(rawContext, cleanText);
          if (summary && summary.reply) workContext = `【工作轨迹子代理总结】\n${String(summary.reply).slice(0, 4000)}`;
        }
      } catch (error) {
        console.error(`工作轨迹总结子代理失败：${error.message}`);
      }
    }
    const response = await api.chatStream(cleanText, chatSessionId, status => {
      if (widget && !widget.isDestroyed()) widget.webContents.send('chat:status', status);
      setTrayState(true, trayLabelForStatus(status));
    }, undefined, files, workContext);
    chatSessionId = response.session_id || chatSessionId;
    return response;
  } finally {
    endBusy();
  }
});
ipcMain.handle('assistant:upload', async (_event, payload) => {
  await ensureServer();
  return api.uploadAttachment(payload || {});
});
ipcMain.handle('skills:list', async () => {
  await ensureServer();
  return api.listSkills();
});
ipcMain.handle('agents:list', async (_event, refresh) => { await ensureServer(); return api.listAgents(Boolean(refresh)); });
ipcMain.handle('agents:open', async (_event, agentId) => {
  await ensureServer();
  return api.openAgent(String(agentId || ''));
});
ipcMain.handle('agents:choose-directory', async () => {
  const parent = agentWindow && !agentWindow.isDestroyed() ? agentWindow : widget;
  const result = await dialog.showOpenDialog(parent, { title: '选择 Agent 的项目文件夹', properties: ['openDirectory'] });
  return result.canceled ? '' : (result.filePaths[0] || '');
});
ipcMain.handle('agents:delegate', async (_event, agentId, task, workDir) => {
  await ensureServer();
  const id = String(agentId || '');
  const result = await api.delegateAgent(id, String(task || ''), String(workDir || ''));
  const listing = await api.listAgents();
  const agent = (listing.agents || []).find(item => item.id === id);
  if (widget && !widget.isDestroyed()) widget.webContents.send('agents:delegated', { name: agent ? agent.name : id, result });
  return result;
});
ipcMain.handle('agents:clarify', async (_event, sessionId, answer, questionId) => {
  await ensureServer();
  const result = await api.answerAgent(
    String(sessionId || ''), String(answer || ''), String(questionId || ''),
  );
  if (widget && !widget.isDestroyed()) {
    widget.webContents.send('agents:delegated', { name: 'BMA', result });
  }
  return result;
});
ipcMain.handle('agents:register-a2a', async (_event, endpoint) => {
  await ensureServer();
  return api.registerA2A(endpoint || {});
});
ipcMain.handle('agents:providers', async (_event, appType) => {
  await ensureServer();
  return api.agentProviders(String(appType || ''));
});
ipcMain.handle('agents:switch-provider', async (_event, appType, providerId) => {
  await ensureServer();
  return api.switchAgentProvider(String(appType || ''), String(providerId || ''));
});
ipcMain.handle('settings:get-llm', async () => { await ensureServer(); return api.getLLMSettings(); });
// 完整设置：当前模型 + 模型方案 + 搜索密钥 + 中继（供设置页一次性渲染）
ipcMain.handle('settings:get-all', async () => { await ensureServer(); return api.getSettings(); });
ipcMain.handle('settings:save-model', async (_event, model) => { await ensureServer(); return api.saveModel(model); });
ipcMain.handle('settings:activate-model', async (_event, id) => { await ensureServer(); return api.activateModel(id); });
ipcMain.handle('settings:delete-model', async (_event, id) => { await ensureServer(); return api.deleteModel(id); });
ipcMain.handle('settings:save-search', async (_event, search) => { await ensureServer(); return api.saveSearch(search); });
ipcMain.handle('settings:save-relay', async (_event, relay) => { await ensureServer(); return api.saveRelay(relay); });
ipcMain.handle('relay:status', async () => { await ensureServer(); return api.relayStatus(); });
ipcMain.handle('relay:pair-code', async () => { await ensureServer(); return api.relayPairCode(); });
ipcMain.handle('relay:channels', async () => { await ensureServer(); return api.relayChannels(); });
ipcMain.handle('relay:revoke', async (_event, channelId) => { await ensureServer(); return api.revokeRelayChannel(channelId); });
ipcMain.handle('settings:save-llm', async (_event, settings) => { await ensureServer(); return api.saveLLMSettings(settings); });
ipcMain.handle('settings:get-desktop', () => ({
  activityOpacity: validActivityOpacity(readSettings().activityOpacity ?? DEFAULT_ACTIVITY_OPACITY),
  activityVisible: readSettings().activityVisible !== false,
  activityCollapsed,
  theme: normalizeTheme(readSettings().theme),
}));
ipcMain.handle('settings:set-theme', (_event, value) => {
  const theme = applyTheme(value);
  saveSettings({ theme });
  broadcast('theme:changed', theme);
  return { theme };
});
ipcMain.handle('settings:set-activity-opacity', (_event, value) => {
  const activityOpacity = validActivityOpacity(value);
  saveSettings({ activityOpacity });
  broadcast('activity:opacity', activityOpacity);
  return { activityOpacity };
});
ipcMain.handle('activity:set-visible', (_event, value) => {
  const visible = setActivityVisible(value);
  if (visible && activityCollapsed) setActivityCollapsed(false);
  return visible;
});
ipcMain.handle('collaboration:set', async (_event, enabled) => {
  const status = await collaboration.setEnabled(enabled);
  await syncActivityRecording();
  saveSettings({ collaborationEnabled: status.enabled, workspace });
  refreshTrayMenu();
  return { ...status, ...activityTrail.status() };
});
ipcMain.handle('collaboration:status', () => ({ ...collaboration.status(), ...activityTrail.status(), workspace }));
ipcMain.handle('collaboration:summarize-now', () => runSummarize());
ipcMain.handle('activity:list', () => activityTrail.list());
ipcMain.handle('activity:summaries', (_event, sessionId) => activityTrail.listSummaryPhases(String(sessionId || '')));
ipcMain.handle('activity:evidence', (_event, id) => activityTrail.evidence(id));
ipcMain.handle('activity:clear', () => {
  activityTrail.clear();
  if (collaboration) collaboration.clear();
  return activityTrail.status();
});
ipcMain.handle('activity:set-collapsed', (_event, value) => ({ collapsed: setActivityCollapsed(value), edge: activityDock }));
ipcMain.handle('capture:shadow-status', () => screenpipeShadow.status());
ipcMain.handle('workspace:connect', async () => {
  const result = await dialog.showOpenDialog(widget, { title: '授权一个 Git 项目目录', properties: ['openDirectory'] });
  if (result.canceled || !result.filePaths[0]) return null;
  const evidence = await readGitEvidence(result.filePaths[0]);
  workspace = result.filePaths[0];
  saveSettings({ collaborationEnabled: collaboration.enabled, workspace });
  await activityTrail.record({ type: 'connector', app: 'Git', title: path.basename(workspace), detail: evidence.summary, at: Date.now() }, false);
  return evidence;
});
ipcMain.handle('workspace:evidence', () => {
  if (!workspace) throw new Error('尚未授权项目目录');
  return readGitEvidence(workspace);
});

// 挂件关闭只收起；只有托盘菜单的“退出”会结束后台进程。
app.on('window-all-closed', event => event.preventDefault());
