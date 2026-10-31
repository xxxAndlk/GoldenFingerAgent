'use strict';

const { InputMonitor } = require('../input-monitor');
const errors = [];
const monitor = new InputMonitor({ onEvent: () => {}, onError: error => errors.push(error) });
monitor.start();
setTimeout(() => {
  const running = Boolean(monitor.process);
  monitor.stop();
  if (!running || errors.length) {
    console.error(errors.join('\n') || '输入事件监听进程未能启动');
    process.exitCode = 1;
  } else {
    console.log('INPUT_MONITOR_OK no raw text capture');
  }
}, 3000);
