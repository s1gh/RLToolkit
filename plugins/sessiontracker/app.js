// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  RLT.plugin.register({
    init() {
      const root = document.getElementById('root');
      if (root) root.textContent = 'session tracker loading…';
    },
  });
})();
