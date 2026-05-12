// Widget control + sizing helpers.
//
// Window-shape ops (size/anchor/margin/opacity/visible) only make sense
// when the plugin is the sole tenant of an OS window — i.e. the Tauri
// desktop overlay. Outside Tauri they no-op.
//
// Sizing helpers (autoSize, fitWidth) work in both contexts: in Tauri
// they call the desktop window-resize command directly; in a hosted
// iframe (web overlay aggregator, OBS browser source) they post the
// measured size to the parent, which honors the message only on axes
// the manifest declared as "auto" (see overlay.html).
//
// `teardownWatchers` is exported for the pagehide handler in index.js
// to call alongside the bus.closeEventSource() so we don't leak
// observers when the page navigates away.

function resolveTarget(target) {
  if (target instanceof Element) return target;
  if (typeof target === 'string') {
    return document.querySelector(target) || document.body;
  }
  return document.body;
}

const activeWatchers = new Set();

export function teardownWatchers() {
  for (const w of activeWatchers) {
    try {
      w.observer.disconnect();
    } catch (_) {
      /* noop: already disposed */
    }
    for (const { type, fn } of w.listeners) {
      try {
        document.removeEventListener(type, fn, true);
      } catch (_) {
        /* noop */
      }
    }
  }
  activeWatchers.clear();
}

function startSizeWatcher(getTarget, flush) {
  let pending = false;
  const schedule = () => {
    if (pending) return;
    pending = true;
    requestAnimationFrame(() => {
      pending = false;
      flush();
    });
  };

  const observer = new ResizeObserver(schedule);
  observer.observe(getTarget());
  if (getTarget() !== document.body && document.body) {
    observer.observe(document.body);
  }

  schedule();

  const listeners = [
    { type: 'animationend', fn: schedule },
    { type: 'transitionend', fn: schedule },
  ];
  for (const { type, fn } of listeners) {
    document.addEventListener(type, fn, true);
  }
  if (document.fonts?.ready) {
    document.fonts.ready.then(schedule);
  }

  const watcher = { observer, listeners };
  activeWatchers.add(watcher);
  return watcher;
}

function stopSizeWatcher(watcher) {
  if (!watcher) return;
  try {
    watcher.observer.disconnect();
  } catch (_) {
    /* noop */
  }
  for (const { type, fn } of watcher.listeners) {
    try {
      document.removeEventListener(type, fn, true);
    } catch (_) {
      /* noop */
    }
  }
  activeWatchers.delete(watcher);
}

export const widget = (function () {
  const inTauri =
    typeof window !== 'undefined' &&
    !!window.__TAURI_INTERNALS__ &&
    typeof window.__TAURI_INTERNALS__.invoke === 'function';

  let autoSizeWatcher = null;
  let fitWidthWatcher = null;
  let fitWidthHighWater = 0;

  function invoke(cmd, args) {
    if (!inTauri) return Promise.resolve(false);
    try {
      return window.__TAURI_INTERNALS__
        .invoke(cmd, args || {})
        .then(() => true)
        .catch((e) => {
          console.warn('[RLT.widget]', cmd, 'failed:', e);
          return false;
        });
    } catch (e) {
      console.warn('[RLT.widget]', cmd, 'threw:', e);
      return Promise.resolve(false);
    }
  }

  // postSize routes a measured size to the host. In Tauri it resizes the
  // OS window via widget_size; in a hosted iframe it tells the parent
  // aggregator, which clamps and only honors auto-declared axes. Returns
  // true if a transport was attempted, false if neither is available.
  function postSize(width, height) {
    if (inTauri) {
      invoke('widget_size', { width, height });
      return true;
    }
    if (typeof window !== 'undefined' && window.parent && window.parent !== window) {
      try {
        window.parent.postMessage({ __rlt_resize__: 1, w: width, h: height }, '*');
        return true;
      } catch (_) {
        return false;
      }
    }
    return false;
  }

  return {
    isHosted() {
      return inTauri;
    },
    size(width, height) {
      return invoke('widget_size', { width: width | 0, height: height | 0 });
    },
    anchor(corner) {
      return invoke('widget_anchor', { anchor: String(corner || 'bottom-left') });
    },
    margin(x, y) {
      return invoke('widget_margin', { x: x | 0, y: y | 0 });
    },
    opacity(o) {
      return invoke('widget_opacity', { opacity: Number(o) });
    },
    visible(v) {
      return invoke('widget_visible', { visible: !!v });
    },
    autoSize(enabled, opts) {
      opts = opts || {};
      const minW = opts.minWidth | 0 || 1;
      const minH = opts.minHeight | 0 || 1;
      const maxW = opts.maxWidth | 0 || 4096;
      const maxH = opts.maxHeight | 0 || 4096;

      stopSizeWatcher(autoSizeWatcher);
      autoSizeWatcher = null;
      if (!enabled) return true;

      let lastW = -1,
        lastH = -1;
      const flush = () => {
        const el = resolveTarget(opts.target);
        if (!el) return;
        const r = el.getBoundingClientRect();
        // A 0x0 rect means the target is hidden (display:none, e.g. the
        // hide_when_unfocused gate flipped off). Reporting that to the
        // host would shrink the iframe to 1x1, which then becomes the
        // viewport when focus returns — body has no room to lay out, so
        // it stays small and the widget never recovers. Skip the post
        // and wait for the next tick when the target is visible again.
        if (r.width === 0 && r.height === 0) return;
        const w = Math.max(minW, Math.min(maxW, Math.ceil(r.width)));
        const h = Math.max(minH, Math.min(maxH, Math.ceil(r.height)));
        if (w === lastW && h === lastH) return;
        lastW = w;
        lastH = h;
        postSize(w, h);
      };

      autoSizeWatcher = startSizeWatcher(() => resolveTarget(opts.target), flush);
      return true;
    },

    fitWidth(opts) {
      opts = opts || {};
      const maxW = opts.maxWidth | 0 || 800;
      const extra = opts.extra | 0 || 0;

      stopSizeWatcher(fitWidthWatcher);
      fitWidthWatcher = null;

      const flush = () => {
        const el = resolveTarget(opts.target);
        if (!el) return;
        const wanted = Math.min(maxW, el.scrollWidth + extra);
        if (wanted <= fitWidthHighWater) return;
        fitWidthHighWater = wanted;
        postSize(wanted, window.innerHeight);
      };

      fitWidthWatcher = startSizeWatcher(() => resolveTarget(opts.target), flush);
      return true;
    },
  };
})();
