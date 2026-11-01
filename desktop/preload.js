'use strict';

const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('desktopAssistant', {
  toggle: expanded => ipcRenderer.invoke('widget:set-expanded', expanded),
  dragStart: (x, y) => ipcRenderer.invoke('widget:drag-start', x, y),
  dragMove: (x, y) => ipcRenderer.invoke('widget:drag-move', x, y),
  dragEnd: () => ipcRenderer.invoke('widget:drag-end'),
  chat: (text, attachments) => ipcRenderer.invoke('assistant:chat', text, attachments),
  uploadAttachment: payload => ipcRenderer.invoke('assistant:upload', payload),
  status: () => ipcRenderer.invoke('assistant:status'),
  mainSession: () => ipcRenderer.invoke('assistant:main-session'),
  listWorkerTasks: threadId => ipcRenderer.invoke('assistant:worker-tasks', threadId || ''),
  cancelWorkerTask: id => ipcRenderer.invoke('assistant:worker-cancel', id),
  steerWorkerTask: (id, text) => ipcRenderer.invoke('assistant:worker-steer', id, text),
  listOutbox: status => ipcRenderer.invoke('assistant:outbox', status || 'pending_approval'),
  approveOutbox: (id, version) => ipcRenderer.invoke('assistant:outbox-approve', id, version),
  rejectOutbox: (id, version, reason) => ipcRenderer.invoke('assistant:outbox-reject', id, version, reason || ''),
  latestBrief: date => ipcRenderer.invoke('assistant:brief-latest', date || ''),
  generateBrief: date => ipcRenderer.invoke('assistant:brief-generate', date || ''),
  markBriefDelivered: id => ipcRenderer.invoke('assistant:brief-delivered', id),
  channelSettings: () => ipcRenderer.invoke('assistant:channel-settings'),
  updateChannelPolicy: policy => ipcRenderer.invoke('assistant:channel-policy', policy),
  getEmailSettings: () => ipcRenderer.invoke('assistant:email-settings'),
  saveEmailSettings: email => ipcRenderer.invoke('assistant:email-save', email),
  openWorkbench: () => ipcRenderer.invoke('ui:open-window', 'workbench'),
  onAgentEvent: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('agent:event', listener);
    return () => ipcRenderer.removeListener('agent:event', listener);
  },
  listSessions: date => ipcRenderer.invoke('assistant:sessions', date || ''),
  sessionMessages: (id, before = '', limit = 50) => ipcRenderer.invoke('assistant:session-messages', id, before, limit),
  useSession: id => ipcRenderer.invoke('assistant:use-session', id || ''),
  setCollaboration: enabled => ipcRenderer.invoke('collaboration:set', enabled),
  collaborationStatus: () => ipcRenderer.invoke('collaboration:status'),
  summarizeNow: () => ipcRenderer.invoke('collaboration:summarize-now'),
  activityList: () => ipcRenderer.invoke('activity:list'),
  activitySummaries: (sessionId = '') => ipcRenderer.invoke('activity:summaries', sessionId),
  activityEvidence: id => ipcRenderer.invoke('activity:evidence', id),
  clearActivity: () => ipcRenderer.invoke('activity:clear'),
  connectWorkspace: () => ipcRenderer.invoke('workspace:connect'),
  gitEvidence: () => ipcRenderer.invoke('workspace:evidence'),
  onActivity: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('activity:event', listener);
    return () => ipcRenderer.removeListener('activity:event', listener);
  },
  onActivityError: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('activity:error', listener);
    return () => ipcRenderer.removeListener('activity:error', listener);
  },
  listAgents: (refresh = false) => ipcRenderer.invoke('agents:list', refresh),
  openWindow: kind => ipcRenderer.invoke('ui:open-window', kind),
  openSettings: () => ipcRenderer.invoke('ui:open-settings'),
  openAgent: agentId => ipcRenderer.invoke('agents:open', agentId),
  delegateAgent: (agentId, task, workDir = '') => ipcRenderer.invoke('agents:delegate', agentId, task, workDir),
  answerAgent: (sessionId, answer, questionId = '') => ipcRenderer.invoke('agents:clarify', sessionId, answer, questionId),
  chooseAgentDirectory: () => ipcRenderer.invoke('agents:choose-directory'),
  onAgentResult: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('agents:delegated', listener);
    return () => ipcRenderer.removeListener('agents:delegated', listener);
  },
  registerA2A: endpoint => ipcRenderer.invoke('agents:register-a2a', endpoint),
  agentProviders: appType => ipcRenderer.invoke('agents:providers', appType),
  switchAgentProvider: (appType, providerId) => ipcRenderer.invoke('agents:switch-provider', appType, providerId),
  relayStatus: () => ipcRenderer.invoke('relay:status'),
  relayPairCode: () => ipcRenderer.invoke('relay:pair-code'),
  relayChannels: () => ipcRenderer.invoke('relay:channels'),
  revokeRelayChannel: channelId => ipcRenderer.invoke('relay:revoke', channelId),
  getLLMSettings: () => ipcRenderer.invoke('settings:get-llm'),
  listSkills: () => ipcRenderer.invoke('skills:list'),
  getSettings: () => ipcRenderer.invoke('settings:get-all'),
  saveLLMSettings: settings => ipcRenderer.invoke('settings:save-llm', settings),
  saveModel: model => ipcRenderer.invoke('settings:save-model', model),
  activateModel: id => ipcRenderer.invoke('settings:activate-model', id),
  deleteModel: id => ipcRenderer.invoke('settings:delete-model', id),
  saveSearch: search => ipcRenderer.invoke('settings:save-search', search),
  saveRelay: relay => ipcRenderer.invoke('settings:save-relay', relay),
  getDesktopSettings: () => ipcRenderer.invoke('settings:get-desktop'),
  setTheme: theme => ipcRenderer.invoke('settings:set-theme', theme),
  onThemeChanged: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('theme:changed', listener);
    return () => ipcRenderer.removeListener('theme:changed', listener);
  },
  onChatStatus: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('chat:status', listener);
    return () => ipcRenderer.removeListener('chat:status', listener);
  },
  onChatSession: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('chat:session', listener);
    return () => ipcRenderer.removeListener('chat:session', listener);
  },
  setActivityOpacity: value => ipcRenderer.invoke('settings:set-activity-opacity', value),
  setActivityVisible: value => ipcRenderer.invoke('activity:set-visible', value),
  setActivityCollapsed: value => ipcRenderer.invoke('activity:set-collapsed', value),
  onActivityCollapsed: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('activity:collapsed', listener);
    return () => ipcRenderer.removeListener('activity:collapsed', listener);
  },
  onActivityHandleSide: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('activity:handle-side', listener);
    return () => ipcRenderer.removeListener('activity:handle-side', listener);
  },
  onWidgetDock: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('widget:dock', listener);
    return () => ipcRenderer.removeListener('widget:dock', listener);
  },
  onActivityVisible: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('activity:visible', listener);
    return () => ipcRenderer.removeListener('activity:visible', listener);
  },
  onActivityOpacity: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('activity:opacity', listener);
    return () => ipcRenderer.removeListener('activity:opacity', listener);
  },
  onSettingsOpen: callback => {
    const listener = () => callback();
    ipcRenderer.on('settings:open', listener);
    return () => ipcRenderer.removeListener('settings:open', listener);
  },
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
