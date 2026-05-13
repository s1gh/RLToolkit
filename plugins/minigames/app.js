// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  if (typeof root === 'undefined' || !root.RLT) return;

  // Per-view renderers wired in tasks 7 (overlay), 8 (dashboard), 9 (settings).
  RLT.plugin.register({
    init() {
      const el = document.getElementById('root');
      if (!el) return;
      if (RLT.isOverlay)      el.textContent = '';
      if (RLT.isDashboard)    el.textContent = '';
      if (RLT.isSettingsView) el.textContent = '';
    },
  });
})(typeof window === 'undefined' ? globalThis : window);
