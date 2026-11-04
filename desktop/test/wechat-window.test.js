'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { parseWeChatWindows, selectWeChatWindow } = require('../wechat-window');

test('WeChat window parser accepts one or many windows and rejects tiny handles', () => {
  const single = parseWeChatWindows(JSON.stringify({
    title: '微信', process: 'Weixin', processId: 1, handle: 2, offscreen: false,
    childCount: 0, x: 10, y: 20, width: 800, height: 600,
  }));
  assert.equal(single.length, 1);
  assert.deepEqual(single[0].rect, { x: 10, y: 20, width: 800, height: 600 });

  const many = parseWeChatWindows(JSON.stringify([
    { title: '微信', x: 0, y: 0, width: 100, height: 100, offscreen: false },
    { title: '微信', x: 0, y: 0, width: 1, height: 1, offscreen: false },
  ]));
  assert.equal(many.length, 1);
  assert.equal(selectWeChatWindow(many).rect.width, 100);
  assert.equal(selectWeChatWindow([]), null);
  assert.deepEqual(parseWeChatWindows('not json'), []);
});
