'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { containsSensitiveContent, redactSensitiveContent } = require('../redaction');

test('redaction removes API keys and labeled secrets', () => {
  const raw = 'api_key: "sk-d64b36e02e894c7e8e21b4b052d67d43"\npassword=hello1234';
  assert.equal(containsSensitiveContent(raw), true);
  const result = redactSensitiveContent(raw);
  assert.equal(result.redacted, true);
  assert.doesNotMatch(result.text, /d64b36e02e894c7e8e21b4b052d67d43|hello1234/);
  assert.match(result.text, /已隐藏/);
});

test('redaction leaves ordinary work notes untouched', () => {
  const raw = '今天在 VS Code 修改了 settings.json，并检查了工作轨迹面板。';
  assert.equal(containsSensitiveContent(raw), false);
  const result = redactSensitiveContent(raw);
  assert.equal(result.redacted, false);
  assert.equal(result.text, raw);
});

test('redaction catches long unlabeled tokens', () => {
  const raw = 'token 0123456789abcdef0123456789abcdef';
  assert.equal(containsSensitiveContent(raw), true);
  const result = redactSensitiveContent(raw);
  assert.doesNotMatch(result.text, /0123456789abcdef0123456789abcdef/);
});

test('redaction treats Chinese key-management pages as sensitive', () => {
  assert.equal(containsSensitiveContent('API 密钥\n管理你账号的 API 密钥。'), true);
});

test('redaction handles OCR-split labels and values', () => {
  const raw = '"api_key"\nsk 一 d64b36 92s894C7s8e21b4b952d67d43\n"protocol"';
  assert.equal(containsSensitiveContent(raw), true);
  const result = redactSensitiveContent(raw);
  assert.equal(result.redacted, true);
  assert.doesNotMatch(result.text, /d64b36|92s894/);
  assert.match(result.text, /已隐藏/);
});
