'use strict';

const { nativeImage } = require('electron');

function compareSignatures(previous, current) {
  if (!previous || !current || previous.length !== current.length) return { changed: true, ratio: 1, mean: 255 };
  let changed = 0;
  let total = 0;
  const pixels = current.length / 4;
  for (let offset = 0; offset < current.length; offset += 4) {
    const delta = Math.max(
      Math.abs(current[offset] - previous[offset]),
      Math.abs(current[offset + 1] - previous[offset + 1]),
      Math.abs(current[offset + 2] - previous[offset + 2]),
    );
    total += delta;
    if (delta > 28) changed += 1;
  }
  const ratio = changed / pixels;
  const mean = total / pixels;
  // 忽略鼠标悬停、光标闪烁和小图标动画，只让实质内容变化触发 OCR。
  return { changed: ratio >= 0.02 && mean >= 2, ratio, mean };
}

function signature(imagePath) {
  const image = nativeImage.createFromPath(imagePath);
  if (image.isEmpty()) throw new Error('无法读取截图进行画面比较');
  return image.resize({ width: 192, height: 120 }).toBitmap();
}

const previousByWindow = new Map();

async function hasMeaningfulVisualChange(windowKey, imagePath) {
  const current = signature(imagePath);
  const previous = previousByWindow.get(windowKey);
  const result = compareSignatures(previous, current);
  if (result.changed) previousByWindow.set(windowKey, current);
  return result;
}

module.exports = { hasMeaningfulVisualChange, compareSignatures };
