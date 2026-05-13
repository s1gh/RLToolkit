// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  root.MinigamesReducers = {};

  if (typeof window === 'undefined' || !window.RLT) return;
  // SDK-glue wiring added in later tasks.
})(typeof window === 'undefined' ? globalThis : window);
