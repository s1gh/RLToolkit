// Demolitions 2 — overlay + dashboard plugin. See
// docs/superpowers/specs/2026-05-10-demos2-design.md for design.
//
// Loaded as a classic <script>, not an ES module.
// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const isOverlay = RLT.isOverlay;

  function render() {
    // Per-surface render branches land in later tasks.
  }

  RLT.plugin.register({
    init() {
      if (isOverlay && RLT.widget.isHosted()) {
        RLT.widget.fitWidth({ target: '.ov', maxWidth: 600, extra: 8 });
      }
    },
    ready() {
      render();
    },
  });
})();
