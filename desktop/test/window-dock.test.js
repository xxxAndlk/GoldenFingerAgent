'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  clampBounds,
  snapBounds,
  dockEdge,
  nearestEdge,
  edgeTabBounds,
  anchorExpandedBounds,
} = require('../window-dock');

const AREA = { x: 0, y: 0, width: 1920, height: 1040 };
const ORB = { x: 1800, y: 900, width: 76, height: 76 };

test('clampBounds keeps a window inside the work area', () => {
  assert.deepEqual(clampBounds({ x: -50, y: -20, width: 460, height: 680 }, AREA), {
    x: 0,
    y: 0,
    width: 460,
    height: 680,
  });
  assert.deepEqual(clampBounds({ x: 1900, y: 1030, width: 460, height: 680 }, AREA), {
    x: 1460,
    y: 360,
    width: 460,
    height: 680,
  });
});

test('snapBounds pulls a window to the nearest edge within threshold', () => {
  assert.deepEqual(snapBounds({ x: 12, y: 300, width: 460, height: 300 }, AREA), { x: 0, y: 300 });
  assert.deepEqual(snapBounds({ x: 1448, y: 300, width: 460, height: 300 }, AREA), { x: 1460, y: 300 });
  assert.deepEqual(snapBounds({ x: 1448, y: 728, width: 460, height: 300 }, AREA), { x: 1460, y: 740 });
  assert.deepEqual(snapBounds({ x: 1300, y: 300, width: 460, height: 300 }, AREA), { x: 1300, y: 300 });
});

test('snapBounds clamps off-screen windows with oversized dimensions', () => {
  assert.deepEqual(snapBounds({ x: -200, y: -100, width: 2400, height: 2000 }, AREA), { x: 0, y: 0 });
});

test('dockEdge only reports edges the window is flush with', () => {
  assert.equal(dockEdge({ x: 1460, y: 300, width: 460, height: 680 }, AREA), 'right');
  assert.equal(dockEdge({ x: 0, y: 300, width: 460, height: 680 }, AREA), 'left');
  assert.equal(dockEdge({ x: 700, y: 0, width: 460, height: 680 }, AREA), 'top');
  assert.equal(dockEdge({ x: 700, y: 360, width: 460, height: 680 }, AREA), 'bottom');
  assert.equal(dockEdge({ x: 700, y: 100, width: 460, height: 680 }, AREA), null);
});

test('nearestEdge picks the closest side even without docking', () => {
  assert.equal(nearestEdge({ x: 700, y: 100, width: 460, height: 680 }, AREA), 'top');
  assert.equal(nearestEdge({ x: 700, y: 400, width: 460, height: 680 }, AREA), 'bottom');
});

test('edgeTabBounds produces flush vertical and horizontal tabs', () => {
  assert.deepEqual(edgeTabBounds(ORB, AREA, 'right'), { x: 1884, y: 872, width: 36, height: 132 });
  assert.deepEqual(edgeTabBounds(ORB, AREA, 'left'), { x: 0, y: 872, width: 36, height: 132 });
  assert.deepEqual(edgeTabBounds(ORB, AREA, 'bottom'), { x: 1772, y: 1004, width: 132, height: 36 });
  assert.deepEqual(edgeTabBounds(ORB, AREA, 'top'), { x: 1772, y: 0, width: 132, height: 36 });
});

test('anchorExpandedBounds aligns the panel with a docked orb', () => {
  const area = { x: 0, y: 0, width: 1920, height: 1040 };
  const docked = { x: 1844, y: 800, width: 76, height: 76 };
  assert.deepEqual(anchorExpandedBounds(docked, { width: 460, height: 680 }, area, 'right'), {
    x: 1460,
    y: 360,
    width: 460,
    height: 680,
  });
  const free = { x: 700, y: 400, width: 76, height: 76 };
  assert.deepEqual(anchorExpandedBounds(free, { width: 460, height: 680 }, area, null), {
    x: 508,
    y: 98,
    width: 460,
    height: 680,
  });
});
