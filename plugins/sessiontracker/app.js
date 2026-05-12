// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  // Pure data layer (testable; no DOM, no SDK)

  function emptyBucket(bootId) {
    return {
      bootId: bootId || '',
      startedAt: new Date().toISOString(),
      results: { wins: 0, losses: 0, last: [] },
      totals:  { goals: 0, saves: 0, demos: 0 },
      modifiers: {
        aerial: 0, bicycle: 0, longGoal: 0, overtime: 0,
        hatTrick: 0, flipReset: 0, backwards: 0, turtle: 0, poolShot: 0,
      },
      ball: { fastestKmh: null },
      crossbar: { hits: 0, hardest: null },
      mmr: { ranked: {}, casual: null },
    };
  }

  const Reducers = {
    emptyBucket,
  };

  root.SessionTrackerReducers = Reducers;

  // SDK / DOM layer (skipped in tests)

  if (typeof RLT === 'undefined') return;

  // SDK registration and rendering live below; added in later tasks.
})(typeof window !== 'undefined' ? window : globalThis);
