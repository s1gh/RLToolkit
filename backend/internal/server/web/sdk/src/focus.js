import { emitter } from './util.js';

// Game-foreground focus detection. The Tauri widget posts
// {__rlt_focus__: true, active: bool} into the webview as the host
// window's focus state changes; we re-emit as a typed change event.

export const focus = (function () {
  const ev = emitter();
  if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
    window.addEventListener('message', (msg) => {
      if (msg?.data?.__rlt_focus__ !== true) return;
      ev.emit('change', !!msg.data.active);
    });
  }
  return {
    onChange(fn) {
      return ev.on('change', fn);
    },
  };
})();
