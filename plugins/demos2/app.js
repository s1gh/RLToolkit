// Demolitions 2 — overlay + dashboard plugin. See
// docs/superpowers/specs/2026-05-10-demos2-design.md for design.
//
// Loaded as a classic <script>, not an ES module.
// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const isOverlay = RLT.isOverlay;

  const COMBO_WINDOW_MS = 20000;
  const BREAK_HOLD_MS = 1000;

  // Streak counts map to a tier. The first five tiers cover x2 → x6+;
  // x8/x10/x12+ escalate further. tierFor(streak) returns the highest
  // tier whose `min` is <= streak, or null when streak < 2.
  const TIERS = [
    { min: 2,  word: 'TAGGED',      cls: 'tier-base' },
    { min: 3,  word: 'SMOKED',      cls: 'tier-glow' },
    { min: 4,  word: 'ANNIHILATED', cls: 'tier-hot' },
    { min: 5,  word: 'OBLITERATED', cls: 'tier-bloom' },
    { min: 6,  word: 'APOCALYPSE',  cls: 'tier-apocalypse' },
    { min: 8,  word: 'ARMAGEDDON',  cls: 'tier-armageddon' },
    { min: 10, word: 'EXTINCTION',  cls: 'tier-extinction' },
    { min: 12, word: 'GENOCIDE',    cls: 'tier-genocide' },
  ];

  function tierFor(streak) {
    let hit = null;
    for (const t of TIERS) {
      if (streak >= t.min) hit = t;
    }
    return hit;
  }

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
