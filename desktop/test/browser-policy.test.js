'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  canonicalUrl, checkUrl, deriveOrigins, pathAllowed, urlOrigin,
} = require('../browser-policy');

test('canonicalUrl normalizes host, hash and query order', () => {
  assert.equal(
    canonicalUrl('HTTPS://Example.COM/a/../b/?b=2&a=1#frag'),
    'https://example.com/b/?a=1&b=2',
  );
  assert.equal(urlOrigin('https://Example.com:443/x'), 'https://example.com');
});

test('checkUrl enforces origin and path', () => {
  const site = {
    allowed_origins: ['https://example.com'],
    allowed_path_prefixes: ['/items/'],
  };
  assert.equal(checkUrl('https://example.com/items/1', site), 'https://example.com/items/1');
  assert.throws(() => checkUrl('https://evil.example.com/items/1', site), /origin/);
  assert.throws(() => checkUrl('https://example.com/private/1', site), /path/);
});

test('pathAllowed requires a path boundary', () => {
  assert.equal(pathAllowed('/items/42', ['/items']), true);
  assert.equal(pathAllowed('/items-other', ['/items']), false);
  assert.equal(pathAllowed('/anything', []), true);
});

test('deriveOrigins deduplicates start URLs', () => {
  assert.deepEqual(
    deriveOrigins(['https://example.com/a', 'https://example.com/b', 'https://other.com/']),
    ['https://example.com', 'https://other.com'],
  );
});
