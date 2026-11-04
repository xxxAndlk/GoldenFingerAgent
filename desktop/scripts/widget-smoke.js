'use strict';

// 聊天窗口冒烟：用 Playwright 驱动真实 Electron 窗口，验证
// 1) 面板打开后落在最底部；
// 2) 「清屏」只隐藏当前消息，不删除，提示条出现；
// 3) 向上滚动能把刚才藏起的消息恢复回来。
// 需要真实桌面会话，不能在 npm test 里跑。

const fs = require('node:fs');
const path = require('node:path');
const { _electron: electron } = require('playwright-core');

function fail(message) {
  console.error(`WIDGET_SMOKE_FAIL ${message}`);
  process.exitCode = 2;
}

(async () => {
  const app = await electron.launch({
    args: [path.join(__dirname, '..')],
    cwd: path.join(__dirname, '..'),
    env: { ...process.env },
  });
  try {
    const window = await app.firstWindow();
    // 面板默认收起，#messages 不可见；先等它挂载，再展开。
    await window.waitForSelector('#messages', { state: 'attached', timeout: 30_000 });
    await window.evaluate(() => {
      const panel = document.querySelector('#panel');
      if (panel && panel.hidden) document.querySelector('#orb').click();
    });
    await window.waitForTimeout(1500);
    // 等首屏内容稳定：会话要拉服务端记录，服务端刚起来时会慢一点。
    let opened = null;
    for (let attempt = 0; attempt < 30; attempt += 1) {
      opened = await window.evaluate(() => {
        const box = document.querySelector('#messages');
        return {
          hidden: Boolean(document.querySelector('#panel').hidden),
          count: box.querySelectorAll('.message').length,
          scrollTop: box.scrollTop,
          scrollHeight: box.scrollHeight,
          clientHeight: box.clientHeight,
        };
      });
      if (!opened.hidden && opened.count > 1) break;
      await window.waitForTimeout(500);
    }

    if (opened.hidden) throw new Error('面板没有展开');

    // 顶部今日简报可以关闭，关掉后不再显示。
    const briefShown = await window.evaluate(() => !document.querySelector('#brief-card').hidden);
    if (briefShown) {
      await window.click('#brief-card-close');
      const briefHidden = await window.evaluate(() => document.querySelector('#brief-card').hidden);
      if (!briefHidden) throw new Error('今日简报关闭无效');
    }

    if (!opened.count) throw new Error('窗口里没有消息，无法验证滚动');
    const distance = opened.scrollHeight - opened.scrollTop - opened.clientHeight;
    if (distance > 8) throw new Error(`打开后不在底部，距底 ${distance}px`);

    // 时间标签贴着自己那侧：机主消息右对齐，回复左对齐。
    const timeAlign = await window.evaluate(() => {
      const out = { user: null, assistant: null };
      for (const label of document.querySelectorAll('#messages .message-time')) {
        const next = label.nextElementSibling;
        if (!next || !next.classList.contains('message')) continue;
        const role = next.classList.contains('user') ? 'user' : 'assistant';
        if (out[role] === null) out[role] = getComputedStyle(label).textAlign;
      }
      return out;
    });
    if (timeAlign.user && timeAlign.user !== 'right') throw new Error(`机主消息时间没有右对齐：${timeAlign.user}`);
    if (timeAlign.assistant && timeAlign.assistant !== 'left') throw new Error(`回复消息时间没有左对齐：${timeAlign.assistant}`);

    const shotDir = path.join(__dirname, '..', 'artifacts');
    fs.mkdirSync(shotDir, { recursive: true });
    await window.screenshot({ path: path.join(shotDir, 'widget-opened.png') });
    await window.click('#clear-screen');
    const justCleared = await window.evaluate(() => {
      const box = document.querySelector('#messages');
      return {
        hintHidden: Boolean(document.querySelector('#clear-hint').hidden),
        scrollTop: box.scrollTop,
        scrollHeight: box.scrollHeight,
        clientHeight: box.clientHeight,
        visible: Array.from(box.querySelectorAll('.message')).filter(el => el.offsetParent !== null).length,
      };
    });
    await window.waitForTimeout(700);
    await window.screenshot({ path: path.join(shotDir, 'widget-cleared.png') });
    const cleared = await window.evaluate(() => {
      const box = document.querySelector('#messages');
      const visible = Array.from(box.querySelectorAll('.message')).filter(el => el.offsetParent !== null);
      return {
        visible: visible.length,
        detail: visible.map(el => `${el.className}::${String(el.textContent || '').slice(0, 24)}`),
        children: Array.from(box.children).map(el => el.className),
        hintHidden: Boolean(document.querySelector('#clear-hint').hidden),
        scrollHeight: box.scrollHeight,
        clientHeight: box.clientHeight,
      };
    });
    if (cleared.visible !== 0) throw new Error(`清屏后仍有 ${cleared.visible} 条消息可见：${cleared.detail.join(' | ')}；容器子节点：${cleared.children.join(',')}`);
    if (cleared.hintHidden) throw new Error(`清屏后没有出现提示条：点击后 ${JSON.stringify(justCleared)}，250ms 后 ${JSON.stringify(cleared)}`);
    if (cleared.scrollHeight > cleared.clientHeight + 8) throw new Error(`清屏后仍被撑高：scrollHeight=${cleared.scrollHeight} clientHeight=${cleared.clientHeight}`);

    await window.evaluate(() => {
      const box = document.querySelector('#messages');
      box.dispatchEvent(new WheelEvent('wheel', { deltaY: -120, bubbles: true, cancelable: true }));
    });
    await window.waitForTimeout(1500);
    const restored = await window.evaluate(() => {
      const box = document.querySelector('#messages');
      const visible = Array.from(box.querySelectorAll('.message')).filter(el => el.offsetParent !== null);
      return {
        visible: visible.length,
        stillHidden: box.querySelectorAll('.cleared-hidden').length,
        spacers: box.querySelectorAll('.clear-spacer').length,
        scrollTop: box.scrollTop,
        hintHidden: Boolean(document.querySelector('#clear-hint').hidden),
      };
    });
    if (restored.visible === 0) throw new Error(`向上滚动没有恢复被隐藏的消息：${JSON.stringify(restored)}`);
    if (!restored.hintHidden) throw new Error('恢复后提示条没有消失');

    // 往上一格加载更早一页，让窗口里的消息超过一页。
    await window.evaluate(() => {
      const box = document.querySelector('#messages');
      box.style.scrollBehavior = 'auto';
      box.scrollTop = 0;
      box.dispatchEvent(new Event('scroll'));
    });
    await window.waitForTimeout(1500);
    const grown = await window.evaluate(() => document.querySelectorAll('#messages .message').length);

    // 关闭再打开：只保留最近一页，并且落在最底部。
    await window.click('#collapse');
    await window.waitForTimeout(400);
    await window.evaluate(() => document.querySelector('#orb').click());
    await window.waitForTimeout(2000);
    const reopened = await window.evaluate(() => {
      const box = document.querySelector('#messages');
      return {
        count: box.querySelectorAll('.message').length,
        scrollTop: box.scrollTop,
        scrollHeight: box.scrollHeight,
        clientHeight: box.clientHeight,
      };
    });
    if (reopened.count > 50) throw new Error(`重新打开后仍有 ${reopened.count} 条消息，应只保留最近 50 条`);
    const reopenedDistance = reopened.scrollHeight - reopened.scrollTop - reopened.clientHeight;
    if (reopenedDistance > 8) throw new Error(`重新打开后不在底部，距底 ${reopenedDistance}px`);

    console.log(`WIDGET_SMOKE_OK time=${timeAlign.user}/${timeAlign.assistant} brief=${briefShown ? 'dismissed' : 'hidden'} messages=${opened.count} cleared=${cleared.visible} restored=${restored.visible} grown=${grown} reopened=${reopened.count}`);
  } catch (error) {
    fail(error.message);
  } finally {
    await app.close().catch(() => {});
  }
})();
