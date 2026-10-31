'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { compareSignatures } = require('../visual-change');

function frame(value) {
  return Buffer.alloc(192 * 120 * 4, value);
}

test('identical desktop frames never trigger another OCR record', () => {
  const image = frame(80);
  assert.equal(compareSignatures(image, Buffer.from(image)).changed, false);
});

test('tiny pointer or hover changes do not trigger a record', () => {
  const before = frame(80);
  const after = frame(80);
  after[0] = 255;
  assert.equal(compareSignatures(before, after).changed, false);
});

test('meaningful page changes trigger content collection', () => {
  const before = frame(80);
  const after = frame(80);
  after.fill(220, 0, 192 * 120 * 4 / 2);
  assert.equal(compareSignatures(before, after).changed, true);
});
