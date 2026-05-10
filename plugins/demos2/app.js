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

  // Bank time — only ticks during `live`. Replays, kickoff countdowns,
  // and pauses do not burn the combo window. lastTickAt is performance.now()
  // at the last bank advance; bankNow() advances the bank to "now" if we
  // are currently in a playable phase, then returns the bank value.
  let playableElapsed = 0;
  let lastTickAt = performance.now();
  let inPlayablePhase = false;

  function bankNow() {
    if (inPlayablePhase) {
      const now = performance.now();
      playableElapsed += now - lastTickAt;
      lastTickAt = now;
    }
    return playableElapsed;
  }

  function setPlayablePhase(active) {
    if (active === inPlayablePhase) return;
    // Settle the bank up to current time before flipping the gate.
    if (inPlayablePhase) bankNow();
    inPlayablePhase = active;
    lastTickAt = performance.now();
  }

  // Combo state. lastDemoAt is bank time (not wall clock) so the
  // 20s window only burns during `live`. currentStreak is 0 between
  // combos and 1..N during an active streak. lastVictimName is the
  // victim of the most recent demo of the active streak. lastMatchVictim
  // survives streak resets and is only cleared on match-guid change.
  let currentStreak = 0;
  let lastDemoAt = 0;
  let lastVictimName = '';
  let lastMatchVictim = '';

  // All-time persistent state, hydrated from the per-plugin store in
  // ready(). bestStreakWord is derived from bestStreak via tierFor() and
  // NOT persisted separately.
  let totals = {};                // { [playerId]: { name, count } }
  let bestStreak = 0;
  let bestStreakWord = '';

  // Per-match state. Reset on match-guid change.
  let current = {};               // { [playerId]: { name, count } }
  let currentGuid = null;

  // Hydration gate. _PlayerDemolished events that arrive before the
  // store load resolves are buffered here and drained at the end of
  // ready() with isReplay=true so they don't double-count against
  // totals (those demos already fired live when they originally happened).
  let hydrated = false;
  const pendingDemos = [];

  // Streak survives these phases. Anything else (ended/none/lobby/podium)
  // resets the combo state.
  const KEEP_STREAK = new Set(['live', 'countdown', 'replay', 'paused']);

  // rAF handle for the active drain loop. Non-zero means the loop is armed.
  let activeRafHandle = 0;

  function totalCount() {
    let n = 0;
    for (const k in totals) n += totals[k].count;
    return n;
  }

  function matchDemoCount() {
    let n = 0;
    for (const k in current) n += current[k].count;
    return n;
  }

  function resetComboState() {
    currentStreak = 0;
    lastDemoAt = 0;
    lastVictimName = '';
  }

  function cleanupMatchState() {
    current = {};
    currentGuid = null;
    lastMatchVictim = '';
    resetComboState();
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;',
      '<': '&lt;',
      '>': '&gt;',
      '"': '&quot;',
      "'": '&#39;',
    }[c]));
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
