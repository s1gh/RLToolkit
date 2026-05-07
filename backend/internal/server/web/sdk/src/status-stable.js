import { emitter } from './util.js';
import { bus, getStatus } from './bus.js';

// Stable connection status (debounced). Smooths the raw `status` event
// stream so plugins that paint a status pill don't flicker through a
// 1-second blip when the SSE reconnects.
const STATUS_DOWN_DEBOUNCE_MS = 3000;

export const statusStableState = (function () {
  const ev = emitter();
  let stable = getStatus();
  let pending = null;

  bus.on('_status', (s) => {
    if (s === 'connected') {
      if (pending) {
        clearTimeout(pending);
        pending = null;
      }
      if (stable !== s) {
        stable = s;
        ev.emit('change', stable);
      }
      return;
    }
    if (stable !== 'connected') {
      if (stable !== s) {
        stable = s;
        ev.emit('change', stable);
      }
      return;
    }
    if (pending) clearTimeout(pending);
    pending = setTimeout(() => {
      pending = null;
      if (stable !== s) {
        stable = s;
        ev.emit('change', stable);
      }
    }, STATUS_DOWN_DEBOUNCE_MS);
  });

  return {
    get() {
      return stable;
    },
    onChange(fn) {
      return ev.on('change', fn);
    },
  };
})();
