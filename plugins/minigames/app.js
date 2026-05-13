// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  if (typeof window === 'undefined' || !window.RLT) return;

  RLT.plugin.register({
    init() {
      const root = document.getElementById('root');
      if (!root) return;
      if (RLT.isOverlay)    root.textContent = '';
      if (RLT.isDashboard)  root.textContent = '';
      if (RLT.isSettingsView) root.textContent = '';
    },
  });
})();
