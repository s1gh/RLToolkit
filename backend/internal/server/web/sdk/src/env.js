// Boot-time environment: URL params + view flags + plugin name.
// Pure module — read once, never mutated. Other modules import these
// constants directly.

export const urlParams = new URLSearchParams(location.search);
export const isSettingsView = urlParams.has('settings');
export const isOverlay = urlParams.has('overlay');
export const hostedBus = urlParams.has('__rlt_hosted');

export const pluginName = (function discover() {
  let name = 'unknown';
  try {
    const cur = document.currentScript;
    if (cur?.dataset?.plugin) {
      name = cur.dataset.plugin;
    } else {
      const m = location.pathname.match(/\/plugins\/([^/]+)\//);
      if (m) name = m[1];
    }
  } catch (_) {
    /* noop */
  }
  return name;
})();
