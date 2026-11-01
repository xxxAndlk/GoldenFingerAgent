'use strict';

// 贴边与收起几何：三个浮窗共用。坐标与尺寸都是 Electron 的 DIP 单位。
const EDGE_SNAP = 26;
const EDGE_OVERFLOW_RATIO = 0.2;
const VERTICAL_EDGES = new Set(['left', 'right']);

function clamp(value, min, max) {
  return Math.max(min, Math.min(value, max));
}

// clampBounds 把窗口完整放进可用区域；窗口比区域大时先收缩尺寸。
function clampBounds(bounds, area) {
  const width = Math.min(bounds.width, area.width);
  const height = Math.min(bounds.height, area.height);
  return {
    width,
    height,
    x: clamp(bounds.x, area.x, area.x + area.width - width),
    y: clamp(bounds.y, area.y, area.y + area.height - height),
  };
}

function edgeDistances(bounds, area) {
  return [
    { edge: 'left', distance: Math.abs(bounds.x - area.x) },
    { edge: 'right', distance: Math.abs(bounds.x + bounds.width - (area.x + area.width)) },
    { edge: 'top', distance: Math.abs(bounds.y - area.y) },
    { edge: 'bottom', distance: Math.abs(bounds.y + bounds.height - (area.y + area.height)) },
  ].sort((a, b) => a.distance - b.distance);
}

// snapBounds 在阈值内把窗口吸到屏幕边缘，返回新的左上角坐标。
function snapBounds(bounds, area, threshold = EDGE_SNAP) {
  const maxX = area.x + area.width - bounds.width;
  const maxY = area.y + area.height - bounds.height;
  let x = clamp(bounds.x, area.x, maxX);
  let y = clamp(bounds.y, area.y, maxY);
  if (Math.abs(x - area.x) <= threshold) x = area.x;
  else if (Math.abs(x - maxX) <= threshold) x = maxX;
  if (Math.abs(y - area.y) <= threshold) y = area.y;
  else if (Math.abs(y - maxY) <= threshold) y = maxY;
  return { x, y };
}

// dockEdge 只在窗口已经贴住某条边时返回边名，用于贴边样式与收起定位。
function dockEdge(bounds, area, tolerance = 1) {
  const [nearest] = edgeDistances(bounds, area);
  return nearest.distance <= tolerance ? nearest.edge : null;
}

function nearestEdge(bounds, area) {
  return edgeDistances(bounds, area)[0].edge;
}

// overflowEdge 检测窗口被拖出可用区域超过比例阈值的方向；默认为窗口尺寸的 20%。
function overflowEdge(bounds, area, ratio = EDGE_OVERFLOW_RATIO) {
  const candidates = [
    { edge: 'left', amount: Math.max(0, area.x - bounds.x), limit: bounds.width * ratio },
    { edge: 'right', amount: Math.max(0, bounds.x + bounds.width - (area.x + area.width)), limit: bounds.width * ratio },
    { edge: 'top', amount: Math.max(0, area.y - bounds.y), limit: bounds.height * ratio },
    { edge: 'bottom', amount: Math.max(0, bounds.y + bounds.height - (area.y + area.height)), limit: bounds.height * ratio },
  ];
  return candidates
    .filter(item => item.amount > 0 && item.amount >= item.limit)
    .sort((a, b) => b.amount - a.amount)[0]?.edge || null;
}

// edgeTabBounds 把窗口收成贴边小条：左右边生成竖条，上下边生成横条。
function edgeTabBounds(bounds, area, edge, size = { along: 132, cross: 36 }) {
  const vertical = VERTICAL_EDGES.has(edge);
  const width = vertical ? size.cross : size.along;
  const height = vertical ? size.along : size.cross;
  const centerX = bounds.x + Math.round(bounds.width / 2);
  const centerY = bounds.y + Math.round(bounds.height / 2);
  let x;
  let y;
  if (edge === 'left') x = area.x;
  else if (edge === 'right') x = area.x + area.width - width;
  else x = clamp(centerX - Math.round(width / 2), area.x, area.x + area.width - width);
  if (edge === 'top') y = area.y;
  else if (edge === 'bottom') y = area.y + area.height - height;
  else y = clamp(centerY - Math.round(height / 2), area.y, area.y + area.height - height);
  return { x, y, width, height };
}

// anchorExpandedBounds 以悬浮球为锚点展开面板：贴右时面板右边对齐，贴下时底边对齐。
function anchorExpandedBounds(anchor, size, area, dock = null) {
  const width = Math.min(size.width, area.width);
  const height = Math.min(size.height, area.height);
  let x = anchor.x + Math.round((anchor.width - width) / 2);
  let y = anchor.y + Math.round((anchor.height - height) / 2);
  if (dock === 'left') x = anchor.x;
  else if (dock === 'right') x = anchor.x + anchor.width - width;
  if (dock === 'top') y = anchor.y;
  else if (dock === 'bottom') y = anchor.y + anchor.height - height;
  return {
    width,
    height,
    x: clamp(x, area.x, area.x + area.width - width),
    y: clamp(y, area.y, area.y + area.height - height),
  };
}

module.exports = {
  EDGE_SNAP,
  EDGE_OVERFLOW_RATIO,
  clamp,
  clampBounds,
  snapBounds,
  dockEdge,
  nearestEdge,
  overflowEdge,
  edgeTabBounds,
  anchorExpandedBounds,
};
