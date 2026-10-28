'use strict';

const path = require('node:path');
const fs = require('node:fs');
const { app, BrowserWindow, ipcMain, screen, Notification } = require('electron');
const { AssistantApi } = require('./assistant-api');
const { CollaborationMode } = require('./collaboration');
const { readActiveWindow } = require('./active-window');

const COLLAPSED_SIZE = { width: 76, height: 76 };
const EXPANDED_SIZE = { width: 420, height: 640 };
let widget = null;
let expanded = false;
let collaboration = null;
let chatSessionId = '';
let api = null;

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
  fs.writeFileSync(settingsPath(), JSON.stringify(settings, null, 2));
}

function positionFor(bounds, size) {
  const area = screen.getDisplayMatching(bounds).workArea;
  return {
    x: Math.max(area.x, Math.min(bounds.x + bounds.width - size.width, area.x + area.width - size.width)),
    y: Math.max(area.y, Math.min(bounds.y + bounds.height - size.height, area.y + area.height - size.height)),
  };
}

function setExpanded(next) {
  expanded = Boolean(next);
  if (!widget) return;
  const size = expanded ? EXPANDED_SIZE : COLLAPSED_SIZE;
  const position = positionFor(widget.getBounds(), size);
  widget.setBounds({ ...position, ...size }, true);
  widget.webContents.send('widget:expanded', expanded);
}

function createWidget() {
  const area = screen.getPrimaryDisplay().workArea;
  widget = new BrowserWindow({
    width: COLLAPSED_SIZE.width,
    height: COLLAPSED_SIZE.height,
    x: area.x + area.width - COLLAPSED_SIZE.width - 28,
    y: area.y + area.height - COLLAPSED_SIZE.height - 28,
    frame: false,
    transparent: true,
    resizable: false,
    maximizable: false,
    fullscreenable: false,
    alwaysOnTop: true,
    skipTaskbar: false,
    backgroundColor: '#00000000',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  widget.setAlwaysOnTop(true, 'floating');
  widget.loadFile(path.join(__dirname, 'index.html'));
}

app.whenReady().then(async () => {
  const settings = readSettings();
  api = new AssistantApi({
    baseUrl: process.env.GFA_SERVER_URL || 'http://127.0.0.1:8080',
    token: process.env.GFA_TOKEN || '',
    userId: process.env.GFA_USER_ID || '',
  });
  collaboration = new CollaborationMode({
    readActiveWindow,
    api,
    onSummary: summary => {
      if (widget) widget.webContents.send('collaboration:summary', summary);
      if (summary.reply && Notification.isSupported()) {
        new Notification({ title: '金手指 · 协作总结', body: summary.reply.slice(0, 180) }).show();
      }
    },
  });
  createWidget();
  if (process.argv.includes('--smoke-test')) {
    widget.webContents.once('did-finish-load', () => {
      setExpanded(true);
      setTimeout(async () => {
        const image = await widget.webContents.capturePage();
        const output = path.join(process.cwd(), 'artifacts');
        fs.mkdirSync(output, { recursive: true });
        fs.writeFileSync(path.join(output, 'desktop-panel.png'), image.toPNG());
        console.log('DESKTOP_SMOKE_OK');
        app.exit(0);
      }, 800);
    });
    setTimeout(() => app.exit(2), 15_000);
  } else if (settings.collaborationEnabled) {
    await collaboration.setEnabled(true);
  }

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWidget();
  });
});

ipcMain.handle('widget:set-expanded', (_event, next) => setExpanded(next));
ipcMain.handle('assistant:status', async () => {
  try {
    await api.status();
    return { connected: true };
  } catch (error) {
    return { connected: false, error: error.message };
  }
});
ipcMain.handle('assistant:chat', async (_event, text) => {
  const cleanText = String(text || '').trim().slice(0, 8000);
  if (!cleanText) throw new Error('请输入内容');
  const response = await api.chat(cleanText, chatSessionId);
  chatSessionId = response.session_id || chatSessionId;
  return response;
});
ipcMain.handle('collaboration:set', async (_event, enabled) => {
  const status = await collaboration.setEnabled(enabled);
  saveSettings({ collaborationEnabled: status.enabled });
  return status;
});
ipcMain.handle('collaboration:status', () => collaboration.status());
ipcMain.handle('collaboration:summarize-now', () => collaboration.summarizeNow());

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});
