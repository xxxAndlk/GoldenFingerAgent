'use strict';

const api = window.desktopAssistant;
const button = document.querySelector('#activity-collapse-handle');

api.onActivityHandleSide(side => {
  document.body.dataset.side = side === 'right' ? 'right' : 'left';
});

button.addEventListener('click', () => api.setActivityCollapsed(true));
