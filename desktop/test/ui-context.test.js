'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { parseUiContext } = require('../ui-context');

test('UI context keeps control metadata and password protection', () => {
  assert.deepEqual(parseUiContext('{"name":"","control":"Edit","isPassword":true}'), { name: '', control: 'Edit', isPassword: true });
  assert.deepEqual(parseUiContext(''), {});
});
