'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { selectWindowSource, calculatePageCrop } = require('../window-capture');

test('window capture matches the active window title exactly first', () => {
  const sources = [{ name: 'Project' }, { name: 'Project - VS Code' }];
  assert.equal(selectWindowSource(sources, 'Project - VS Code').name, 'Project - VS Code');
  assert.equal(selectWindowSource(sources, 'Project').name, 'Project');
  assert.equal(selectWindowSource(sources, ''), null);
});

test('page crop maps the current document into screenshot coordinates', () => {
  const crop = calculatePageCrop(
    { x: 50, y: 100, width: 900, height: 650 },
    { x: 0, y: 0, width: 1000, height: 800 },
    { width: 2000, height: 1600 },
  );
  assert.deepEqual(crop, { x: 100, y: 200, width: 1800, height: 1300 });
});

test('page crop clamps coordinates and rejects unusable regions', () => {
  const crop = calculatePageCrop(
    { x: -50, y: 80, width: 900, height: 600 },
    { x: 0, y: 0, width: 1000, height: 800 },
    { width: 1000, height: 800 },
  );
  assert.deepEqual(crop, { x: 0, y: 80, width: 850, height: 600 });
  assert.equal(calculatePageCrop({ x: 0, y: 0, width: 50, height: 40 }, { x: 0, y: 0, width: 1000, height: 800 }, { width: 1000, height: 800 }), null);
  assert.equal(calculatePageCrop(null, { x: 0, y: 0, width: 1000, height: 800 }, { width: 1000, height: 800 }), null);
});
