'use strict';

const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('desktopAssistant', {
  toggle: expanded => ipcRenderer.invoke('widget:set-expanded', expanded),
  chat: text => ipcRenderer.invoke('assistant:chat', text),
  status: () => ipcRenderer.invoke('assistant:status'),
  setCollaboration: enabled => ipcRenderer.invoke('collaboration:set', enabled),
  collaborationStatus: () => ipcRenderer.invoke('collaboration:status'),
  summarizeNow: () => ipcRenderer.invoke('collaboration:summarize-now'),
  onExpanded: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('widget:expanded', listener);
    return () => ipcRenderer.removeListener('widget:expanded', listener);
  },
  onSummary: callback => {
    const listener = (_event, summary) => callback(summary);
    ipcRenderer.on('collaboration:summary', listener);
    return () => ipcRenderer.removeListener('collaboration:summary', listener);
  },
});
