'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { contextForObservation } = require('../observation');

test('work context uses the known project across apps', () => {
  const context = contextForObservation(
    { title: 'main.js - GoldenFingerAgent - Visual Studio Code', content: '修复登录' },
    { projectName: 'GoldenFingerAgent' },
  );
  assert.equal(context.contextKey, 'project:GoldenFingerAgent');
  assert.ok(context.projectCandidates.includes('GoldenFingerAgent'));
});

test('web context is scoped to host and first path segment', () => {
  const context = contextForObservation({ title: '设计文档', url: 'https://example.com/docs/42?x=1' });
  assert.equal(context.contextKey, 'web:example.com/docs');
});

test('unknown context never gets forced into a project', () => {
  const context = contextForObservation({ title: '记事本', url: '' }, { at: Date.UTC(2026, 10, 3) });
  assert.match(context.contextKey, /^activity:unassigned:/);
});
