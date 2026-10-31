'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const Markdown = require('../markdown');

test('Markdown renders common reply formatting', () => {
  const html = Markdown.render('## 结果\n\n- **保存成功**\n- 使用 `npm test`\n\n```js\nconst ok = true;\n```');
  assert.match(html, /<h2>结果<\/h2>/);
  assert.match(html, /<strong>保存成功<\/strong>/);
  assert.match(html, /<code>npm test<\/code>/);
  assert.match(html, /<pre><code class="language-js">const ok = true;/);
});

test('Markdown escapes raw HTML and unsafe links', () => {
  const html = Markdown.render('<img src=x onerror=alert(1)> [危险](javascript:alert(1))');
  assert.doesNotMatch(html, /<img/);
  assert.doesNotMatch(html, /javascript:/);
  assert.match(html, /&lt;img/);
});
