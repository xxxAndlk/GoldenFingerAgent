'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { desktopCapturer } = require('electron');

function selectWindowSource(sources, title) {
  const clean = String(title || '').trim();
  if (!clean) return null;
  return sources.find(source => source.name === clean)
    || sources.find(source => source.name.startsWith(clean) || clean.startsWith(source.name));
}

function validRectangle(rect) {
  return rect && ['x', 'y', 'width', 'height'].every(key => Number.isFinite(Number(rect[key])))
    && Number(rect.width) > 0 && Number(rect.height) > 0;
}

function calculatePageCrop(pageRegion, windowRect, imageSize) {
  if (!validRectangle(pageRegion) || !validRectangle(windowRect)) return null;
  const imageWidth = Number(imageSize && imageSize.width);
  const imageHeight = Number(imageSize && imageSize.height);
  if (!Number.isFinite(imageWidth) || !Number.isFinite(imageHeight) || imageWidth <= 0 || imageHeight <= 0) return null;
  const pageAreaRatio = (Number(pageRegion.width) * Number(pageRegion.height)) / (Number(windowRect.width) * Number(windowRect.height));
  if (Number(pageRegion.width) < 120 || Number(pageRegion.height) < 80 || pageAreaRatio < 0.08 || pageAreaRatio > 1.3) return null;
  const scaleX = imageWidth / Number(windowRect.width);
  const scaleY = imageHeight / Number(windowRect.height);
  const left = Math.max(0, Math.min(imageWidth, Math.floor((Number(pageRegion.x) - Number(windowRect.x)) * scaleX)));
  const top = Math.max(0, Math.min(imageHeight, Math.floor((Number(pageRegion.y) - Number(windowRect.y)) * scaleY)));
  const right = Math.max(0, Math.min(imageWidth, Math.ceil((Number(pageRegion.x) + Number(pageRegion.width) - Number(windowRect.x)) * scaleX)));
  const bottom = Math.max(0, Math.min(imageHeight, Math.ceil((Number(pageRegion.y) + Number(pageRegion.height) - Number(windowRect.y)) * scaleY)));
  if (right - left < 32 || bottom - top < 32) return null;
  return { x: left, y: top, width: right - left, height: bottom - top };
}

async function captureWindow({ title, outputDir, at = Date.now(), pageRegion, windowRect }, capturer = desktopCapturer) {
  if (!title) throw new Error('没有可截图的窗口标题');
  const sources = await capturer.getSources({
    types: ['window'],
    thumbnailSize: { width: 2560, height: 1600 },
    fetchWindowIcons: false,
  });
  const source = selectWindowSource(sources, title);
  if (!source || source.thumbnail.isEmpty()) throw new Error('窗口当前不可见或不允许截图');
  const size = source.thumbnail.getSize();
  const crop = calculatePageCrop(pageRegion, windowRect, size);
  const image = crop ? source.thumbnail.crop(crop) : source.thumbnail;
  fs.mkdirSync(outputDir, { recursive: true });
  const filename = `${new Date(at).toISOString().replace(/[:.]/g, '-')}-${Math.random().toString(36).slice(2, 8)}.png`;
  const target = path.join(outputDir, filename);
  fs.writeFileSync(target, image.toPNG());
  return target;
}

module.exports = { captureWindow, selectWindowSource, calculatePageCrop };
