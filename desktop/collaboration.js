'use strict';

const { ContentMemory } = require('./content-dedup');

const DEFAULT_SCAN_INTERVAL = 30_000;
const DEFAULT_SUMMARY_INTERVAL = 15 * 60_000;

function clean(value, maxLength) {
  return String(value || '').replace(/\s+/g, ' ').trim().slice(0, maxLength);
}

// 总结只陈述，不向用户提问；模型偶尔仍会以问句收尾，这里去掉结尾问题行。
function stripTrailingQuestion(value) {
  let text = String(value || '').trim();
  while (/[？?]\s*$/.test(text)) {
    const lineBreak = text.lastIndexOf('\n');
    const sentenceEnd = Math.max(text.lastIndexOf('。'), text.lastIndexOf('！'), text.lastIndexOf('!'));
    const cut = Math.max(lineBreak, sentenceEnd + 1);
    if (cut <= 0) return '';
    text = text.slice(0, cut).trim();
  }
  return text;
}

function segmentObservations(observations, {
  maxDurationMs = 15 * 60_000,
  idleGapMs = 5 * 60_000,
} = {}) {
  const groups = [];
  let current = null;
  const flush = () => {
    if (current && current.observations.length) groups.push(current);
    current = null;
  };
  for (const observation of observations) {
    const at = Number(observation.at) || Date.now();
    const key = String(observation.contextKey || '').trim();
    if (!current
      || key !== current.contextKey
      || at - current.endedAt > idleGapMs
      || at - current.startedAt > maxDurationMs) {
      flush();
      current = {
        contextKey: key,
        startedAt: at,
        endedAt: at,
        observations: [],
        apps: new Set(),
        projectCandidates: new Set(),
      };
    }
    current.observations.push(observation);
    current.endedAt = Math.max(current.endedAt, at);
    if (observation.app) current.apps.add(String(observation.app));
    for (const candidate of observation.projectCandidates || []) current.projectCandidates.add(String(candidate));
  }
  flush();
  return groups.map(group => ({
    ...group,
    apps: [...group.apps],
    projectCandidates: [...group.projectCandidates],
  }));
}

function summarizeObservations(observations, scanIntervalMs = DEFAULT_SCAN_INTERVAL) {
  const apps = new Map();
  for (const observation of observations) {
    const app = clean(observation.app, 120) || '未知应用';
    if (!apps.has(app)) apps.set(app, { app, scans: 0, titles: [] });
    const entry = apps.get(app);
    entry.scans += 1;
    const title = clean(observation.title, 240);
    if (title && entry.titles[entry.titles.length - 1] !== title) entry.titles.push(title);
  }
  const ranked = [...apps.values()].sort((a, b) => b.scans - a.scans);
  const parts = ranked.map(entry => {
    const minutes = Math.max(1, Math.round(entry.scans * scanIntervalMs / 60_000));
    const titles = entry.titles.slice(-3).join('；');
    return `${entry.app} 约 ${minutes} 分钟${titles ? `（${titles}）` : ''}`;
  });
  const memory = new ContentMemory();
  const actions = [];
  const detailItems = observations.filter(item => item.detail || item.content);
  let used = 0;
  for (let index = 0; index < detailItems.length; index += 1) {
    const item = detailItems[index];
    const content = memory.select(item.content || item.detail, 600);
    if (!content) continue;
    const time = new Date(item.at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    const line = `${time} ${clean(item.app, 80)}《${clean(item.title, 160)}》：${content}`;
    // 单次提示词有上限；超出部分不丢弃，只在这里标注，原始事件已持久化。
    if (used > 0 && used + line.length > 60_000) {
      actions.push(`[其余 ${detailItems.length - index} 条原始记录已保留在轨迹中]`);
      break;
    }
    actions.push(line);
    used += line.length;
  }
  return `本阶段活动软件与窗口：${parts.join('；') || '没有采集到活动窗口'}。${actions.length ? `\n页面可见内容与操作轨迹：\n${actions.join('\n')}` : ''}`;
}

class CollaborationMode {
  constructor({
    readActiveWindow,
    api,
    onSummary = () => {},
    onSummarized = () => {},
    // 总结写入当前对话会话，避免每轮总结都新建一个会话。
    sessionId = () => '',
    onSession = () => {},
    now = () => Date.now(),
    scanIntervalMs = DEFAULT_SCAN_INTERVAL,
    summaryIntervalMs = DEFAULT_SUMMARY_INTERVAL,
    // 上一阶段总结与阶段记录由桌面侧持久化；这里只通过回调读写，方便测试注入。
    loadLastSummary = () => '',
    onPhase = () => {},
    captureEpisode = null,
  }) {
    this.readActiveWindow = readActiveWindow;
    this.api = api;
    this.onSummary = onSummary;
    this.onSummarized = onSummarized;
    this.sessionIdFor = sessionId;
    this.onSession = onSession;
    this.now = now;
    this.scanIntervalMs = scanIntervalMs;
    this.summaryIntervalMs = summaryIntervalMs;
    this.loadLastSummary = loadLastSummary;
    this.onPhase = onPhase;
    this.captureEpisode = captureEpisode;
    this.lastEpisodeAt = 0;
    this.lastEpisodeError = '';
    this.episodeCount = 0;
    this.enabled = false;
    this.observations = [];
    this.pendingSeed = [];
    this.scanTimer = null;
    this.scanning = false;
    this.summaryDue = 0;
    this.lastSummary = '';
    this.lastSummaryAt = 0;
  }

  // 启动时把上次未总结的持久化事件接回待总结队列，避免崩溃/重启丢掉这一段。
  seedPending(observations) {
    const list = Array.isArray(observations) ? observations : [];
    if (this.enabled) {
      this.observations = [...list, ...this.observations].slice(-2000);
    } else {
      this.pendingSeed = list.slice(-2000);
    }
  }

  clear() {
    this.observations = [];
    this.pendingSeed = [];
    this.lastSummary = '';
    this.lastSummaryAt = 0;
    this.summaryDue = this.now() + this.summaryIntervalMs;
  }

  status() {
    return {
      enabled: this.enabled,
      pendingScans: this.observations.length,
      scanIntervalMs: this.scanIntervalMs,
      summaryIntervalMs: this.summaryIntervalMs,
      nextSummaryAt: this.enabled ? new Date(this.summaryDue).toISOString() : null,
      lastSummaryAt: this.lastSummaryAt ? new Date(this.lastSummaryAt).toISOString() : null,
      lastEpisodeAt: this.lastEpisodeAt ? new Date(this.lastEpisodeAt).toISOString() : null,
      lastEpisodeError: this.lastEpisodeError,
      episodeCount: this.episodeCount,
    };
  }

  async setEnabled(enabled) {
    this.enabled = Boolean(enabled);
    clearInterval(this.scanTimer);
    this.scanTimer = null;
    this.observations = this.pendingSeed.length ? this.pendingSeed.slice() : [];
    this.pendingSeed = [];
    if (this.enabled) {
      this.summaryDue = this.now() + this.summaryIntervalMs;
      await this.scanNow();
      this.scanTimer = setInterval(() => this.scanNow(), this.scanIntervalMs);
    }
    return this.status();
  }

  addObservation(observation) {
    if (!this.enabled) return;
    this.observations.push({
      id: observation.id,
      app: clean(observation.app, 120),
      title: clean(observation.title, 240),
      detail: clean(observation.detail, 300),
      content: clean(observation.content, 1800),
      at: observation.at || this.now(),
      url: clean(observation.url, 1000),
      contextKey: clean(observation.contextKey, 300),
      projectCandidates: Array.isArray(observation.projectCandidates)
        ? observation.projectCandidates.map(item => clean(item, 120)).filter(Boolean).slice(0, 8)
        : [],
    });
    this.observations = this.observations.slice(-2000);
  }

  async scanNow() {
    if (!this.enabled || this.scanning) return this.status();
    this.scanning = true;
    try {
      const window = await this.readActiveWindow();
      if (!this.enabled) return this.status();
      if (window) {
        const app = clean(window.owner && window.owner.name, 120);
        const title = clean(window.title, 240);
        const ownApp = ['goldenfinger', 'electron'].includes(app.toLowerCase());
        if (!ownApp && title !== '金手指管家' && title !== '工作轨迹' && title !== 'Agent 与设置') {
          this.observations.push({ app, title, at: this.now() });
        }
      }
      if (this.now() >= this.summaryDue) await this.summarizeNow();
    } catch (error) {
      this.onSummary({ error: `协作扫描暂时失败：${error.message}` });
    } finally {
      this.scanning = false;
    }
    return this.status();
  }

  // saveSharedSummary 把最终阶段总结写入共享长期记忆。
  //
  // 写不进去不影响本阶段总结本身（桌面阶段状态已经落盘），
  // 但必须把失败说出来：不能假装共享已经完成。
  async saveSharedSummary(phase, reply) {
    if (!this.api || typeof this.api.saveSharedMemory !== 'function') return false;
    const text = String(reply || '').trim();
    if (!text) return false;
    const startedAt = new Date(phase.startedAt || this.now()).toLocaleString('zh-CN');
    try {
      await this.api.saveSharedMemory({
        slot: `collab_summary:${phase.id}`,
        text: `协作阶段总结（${startedAt}）：${text}`,
        source: 'collab_summary',
        captured_at: new Date(phase.endedAt || this.now()).toISOString(),
      });
      return true;
    } catch (error) {
      this.onSummary({ error: `协作总结写入共享记忆失败：${error.message}` });
      return false;
    }
  }

  async summarizeNow() {    if (!this.enabled || this.observations.length === 0) {
      return { reply: '这段时间还没有采集到活动窗口。' };
    }
    const summary = summarizeObservations(this.observations, this.scanIntervalMs);
    const observations = this.observations;
    this.observations = [];
    this.summaryDue = this.now() + this.summaryIntervalMs;
    if (!this.lastSummary) {
      try { this.lastSummary = this.loadLastSummary() || ''; } catch { this.lastSummary = ''; }
    }
    const sessionId = this.sessionIdFor();
    const phase = {
      id: `${this.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
      sessionId: sessionId || '',
      startedAt: observations[0]?.at || this.now(),
      endedAt: this.now(),
      eventIds: observations.map(item => item.id).filter(Boolean),
      record: summary,
      summary: '',
      completedAt: 0,
    };
    const previous = this.lastSummary ? `\n上一阶段总结：${this.lastSummary}\n上一阶段已经说过且状态未变的内容不要重复。` : '';
    try {
      await this.api.capture(summary, '', '', 'desktop_collaboration', new Date(this.now()));
      const response = await this.api.chat(
        `以下是用户授权协作模式采集到的页面标题、去重后的页面正文和有效操作。原始截图仅保存在本机且未发送。请只输出工作总结：按“本阶段新增进展、仍在继续”两部分陈述事实，只讲新增内容或状态变化，不要把未采集到的内容猜成事实；若没有实质变化就明确说没有。不要向用户提问，不要请求确认或授权，不要给出下一步建议，不要以问句结尾。${previous}\n${summary}`,
        sessionId,
        AbortSignal.timeout(30_000),
        true,
      );
      if (!sessionId && response.session_id) this.onSession(response.session_id);
      const reply = stripTrailingQuestion(response.reply) || '本阶段没有可陈述的新进展。';
      this.lastSummary = reply;
      this.lastSummaryAt = this.now();
      phase.sessionId = sessionId || response.session_id || this.sessionIdFor() || '';
      phase.summary = reply;
      phase.completedAt = this.lastSummaryAt;
      this.onSummarized(observations, phase);
      this.onPhase(phase);
      // 最终总结进共享长期记忆：手机与电脑只共享这一份记忆，
      // 另一端要能从同一份记忆里检索到这一阶段的结论。
      await this.saveSharedSummary(phase, reply);
      // 按工作上下文拆分 Episode；每个 Episode 有自己的活动 Thread，不把一天的工作混在一起。
      const segments = segmentObservations(observations);
      for (const [index, segment] of segments.entries()) {
        const contextKey = segment.contextKey || '';
        const label = contextKey
          ? contextKey.replace(/^(project|web|doc):/, '')
          : '未归类工作片段';
        const evidenceIds = segment.observations.map(item => item.id).filter(Boolean);
        const payload = {
          episode_id: `${phase.id}-${index}`,
          context_key: contextKey,
          started_at: new Date(segment.startedAt).toISOString(),
          ended_at: new Date(segment.endedAt).toISOString(),
          app: segment.apps[0] || '桌面',
          apps: segment.apps,
          project_candidates: segment.projectCandidates,
          title: `${label} · ${new Date(segment.endedAt).toLocaleString()}`,
          summary: segments.length === 1
            ? reply
            : summarizeObservations(segment.observations, this.scanIntervalMs).slice(0, 4000),
          status: 'completed',
          outputs: [],
          decisions: '',
          issues: '',
          evidence_ids: evidenceIds,
          cursor: evidenceIds.length ? String(evidenceIds[evidenceIds.length - 1]) : '',
          association_confidence: contextKey.startsWith('activity:unassigned') ? 0 : 0.5,
        };
        try {
          if (typeof this.captureEpisode === 'function') await this.captureEpisode(payload);
          else if (typeof this.api.captureActivityEpisode === 'function') await this.api.captureActivityEpisode(payload);
          this.lastEpisodeAt = this.now();
          this.lastEpisodeError = '';
          this.episodeCount += 1;
        } catch (error) {
          this.lastEpisodeError = error.message;
          console.error(`工作片段入队失败：${error.message}`);
        }
      }
      const result = { phase_id: phase.id, record: summary, reply, at: new Date(this.lastSummaryAt).toISOString() };
      this.onSummary(result);
      return result;
    } catch (error) {
      // 总结失败时恢复尚未持久化的采样，等待下个周期，不静默丢数据。
      this.observations = [...observations, ...this.observations].slice(-2000);
      this.summaryDue = this.now() + 60_000;
      const result = { error: `协作总结暂时未送达：${error.message}` };
      this.onSummary(result);
      return result;
    }
  }
}

module.exports = {
  CollaborationMode,
  summarizeObservations,
  segmentObservations,
  stripTrailingQuestion,
  DEFAULT_SCAN_INTERVAL,
  DEFAULT_SUMMARY_INTERVAL,
};
