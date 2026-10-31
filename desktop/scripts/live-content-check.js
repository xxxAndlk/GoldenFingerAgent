'use strict';

const path = require('node:path');
const { app } = require('electron');
const { readActiveWindow } = require('../active-window');
const { readDesktopContent } = require('../desktop-content');

app.whenReady().then(async () => {
  try {
    const window = await readActiveWindow();
    if (!window) throw new Error('没有前台窗口');
    const options = {
      windowKey: `${window.owner.name}\0${window.title}`,
      title: window.title,
      outputDir: path.join(process.cwd(), 'artifacts'),
    };
    const result = await readDesktopContent({ ...options, at: Date.now() });
    console.log(`LIVE_CONTENT_OK source=${result.source} chars=${result.text.length} protected=${result.protectedContent}`);
    await new Promise(resolve => setTimeout(resolve, 1200));
    const second = await readDesktopContent({ ...options, at: Date.now() });
    console.log(`SECOND_FRAME source=${second.source} unchanged=${Boolean(second.unchanged)}`);
    app.exit(result.text || result.protectedContent ? 0 : 2);
  } catch (error) {
    console.error(error);
    app.exit(2);
  }
});
setTimeout(() => app.exit(2), 20_000);
