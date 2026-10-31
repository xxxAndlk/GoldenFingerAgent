'use strict';

const fs = require('node:fs');
const path = require('node:path');

function emptyMetrics() {
  return {
    received: 0, samples: 0, duplicates: 0, duplicateRate: 0,
    chineseSamples: 0, cjkCharacters: 0, textCharacters: 0, cjkRatio: 0,
    tabComparable: 0, tabMatches: 0, tabMatchRate: null,
    inputs: 0, inputRestored: 0, inputRestoreRate: null,
    privacyBlocked: 0, nativeMatched: 0, errors: 0,
    adapterRssBytes: 0, adapterCpuMicros: 0,
    lastPollAt: null, lastError: '',
  };
}

function recalculate(metrics) {
  metrics.duplicateRate = metrics.received ? metrics.duplicates / metrics.received : 0;
  metrics.cjkRatio = metrics.textCharacters ? metrics.cjkCharacters / metrics.textCharacters : 0;
  metrics.tabMatchRate = metrics.tabComparable ? metrics.tabMatches / metrics.tabComparable : null;
  metrics.inputRestoreRate = metrics.inputs ? metrics.inputRestored / metrics.inputs : null;
}

function sameTab(native, candidate) {
  if (native.url && candidate.url) return { comparable: true, match: native.url === candidate.url };
  if (native.title && candidate.title) return { comparable: true, match: native.title === candidate.title };
  return { comparable: false, match: false };
}

// 影子数据只写入独立目录，绝不调用 ActivityTrail.record、聊天或长期记忆接口。
class ScreenpipeShadow {
  constructor({ source, outputDir, now = () => Date.now(), intervalMs = 10000, onError = () => {} }) {
    this.source = source;
    this.outputDir = outputDir;
    this.now = now;
    this.intervalMs = intervalMs;
    this.onError = onError;
    this.enabled = false;
    this.busy = false;
    this.native = [];
    this.metrics = emptyMetrics();
    this.timer = null;
  }

  addNative(observation) {
    this.native.push({ ...observation, evidencePath: undefined, evidenceRef: undefined });
    this.native = this.native.slice(-500);
  }

  compare(observation) {
    const native = this.native.filter(item => String(item.app).toLowerCase() === observation.app.toLowerCase()
      && Math.abs(Number(item.at) - observation.at) <= 15000)
      .sort((a, b) => Math.abs(a.at - observation.at) - Math.abs(b.at - observation.at))[0];
    if (!native) return { nativeMatched: false, tabComparable: false, tabMatch: false };
    const tab = sameTab(native, observation);
    return { nativeMatched: true, tabComparable: tab.comparable, tabMatch: tab.match };
  }

  persistMetrics() {
    recalculate(this.metrics);
    const temporary = path.join(this.outputDir, 'metrics.tmp');
    fs.writeFileSync(temporary, JSON.stringify(this.metrics, null, 2));
    fs.renameSync(temporary, path.join(this.outputDir, 'metrics.json'));
  }

  async poll() {
    if (!this.enabled || this.busy) return this.status();
    this.busy = true;
    const cpu = process.cpuUsage();
    try {
      const report = await this.source.poll();
      if (!this.enabled) return this.status();
      this.metrics.received += report.received || 0;
      this.metrics.duplicates += report.duplicates || 0;
      for (const observation of report.observations || []) {
        if (observation.privacyLevel === 'protected') {
          this.metrics.privacyBlocked += 1;
          continue;
        }
        const comparison = this.compare(observation);
        this.metrics.samples += 1;
        if (comparison.nativeMatched) this.metrics.nativeMatched += 1;
        if (comparison.tabComparable) this.metrics.tabComparable += 1;
        if (comparison.tabMatch) this.metrics.tabMatches += 1;
        if (observation.type === 'submission') {
          this.metrics.inputs += 1;
          if (observation.content) this.metrics.inputRestored += 1;
        }
        const letters = observation.content.match(/[\p{L}\p{N}]/gu) || [];
        const chinese = observation.content.match(/[\u3400-\u9fff]/g) || [];
        this.metrics.textCharacters += letters.length;
        this.metrics.cjkCharacters += chinese.length;
        if (chinese.length) this.metrics.chineseSamples += 1;
        const record = { observation, comparison, recordedAt: new Date(this.now()).toISOString() };
        fs.appendFileSync(path.join(this.outputDir, 'observations.jsonl'), `${JSON.stringify(record)}\n`, { mode: 0o600 });
      }
      this.metrics.lastError = '';
    } catch (error) {
      this.metrics.errors += 1;
      this.metrics.lastError = error.message;
      this.onError(`Screenpipe 影子采集暂不可用：${error.message}`);
    } finally {
      this.metrics.adapterRssBytes = process.memoryUsage().rss;
      const used = process.cpuUsage(cpu);
      this.metrics.adapterCpuMicros += used.user + used.system;
      this.metrics.lastPollAt = new Date(this.now()).toISOString();
      if (this.enabled) this.persistMetrics();
      this.busy = false;
    }
    return this.status();
  }

  async start() {
    if (this.enabled) return this.status();
    this.enabled = true;
    fs.mkdirSync(this.outputDir, { recursive: true, mode: 0o700 });
    this.source.reset();
    await this.poll();
    this.timer = setInterval(() => this.poll(), this.intervalMs);
    return this.status();
  }

  stop() {
    this.enabled = false;
    clearInterval(this.timer);
    this.timer = null;
    this.source.stop();
    return this.status();
  }

  status() {
    return { enabled: this.enabled, mode: 'shadow', outputDir: this.outputDir, metrics: { ...this.metrics } };
  }
}

module.exports = { ScreenpipeShadow, emptyMetrics, sameTab };
