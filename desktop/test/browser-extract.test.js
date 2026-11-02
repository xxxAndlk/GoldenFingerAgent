'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { contentHash, makeWebItem, normalizeText } = require('../browser-extract');

test('normalizeText collapses whitespace and contentHash is stable', () => {
  assert.equal(normalizeText('  第一行\n\n第二行  '), '第一行 第二行');
  assert.equal(contentHash('第一行\n\n第二行'), contentHash('第一行 第二行'));
});

test('makeWebItem keeps source metadata and truncates links/evidence', () => {
  const item = makeWebItem({
    site: { id: 'example', account: 'me' },
    account: 'me',
    url: 'https://example.com/items/1',
    extraction: {
      title: ' 标题 ',
      text: '  正文\n内容 ',
      canonicalUrl: 'https://example.com/items/1',
      extractedBy: 'readability',
      completeness: 'complete',
      capturedAt: '2026-11-01T00:00:00Z',
      evidenceIds: ['a', 'b'],
    },
  });
  assert.equal(item.site_id, 'example');
  assert.equal(item.title, '标题');
  assert.equal(item.text, '正文 内容');
  assert.equal(item.extracted_by, 'readability');
  assert.equal(item.completeness, 'complete');
  assert.equal(item.item_id, 'https://example.com/items/1');
});

test('makeWebItem rejects empty body', () => {
  assert.throws(() => makeWebItem({
    site: { id: 'example' }, url: 'https://example.com', extraction: { text: '   ' },
  }), /正文/);
});
