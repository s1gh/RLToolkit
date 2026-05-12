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

  function applyMatchEnded(bucket, payload, myTeam) {
    if (myTeam !== 0 && myTeam !== 1) return;
    const winner = payload && payload.winnerTeamNum;
    if (winner !== 0 && winner !== 1) return;
    const result = winner === myTeam ? 'win' : 'loss';
    if (result === 'win') bucket.results.wins++;
    else bucket.results.losses++;
    bucket.results.last.push(result);
    if (bucket.results.last.length > 10) bucket.results.last.shift();
  }

  function applyPlayerScoreChanged(bucket, payload) {
    if (!payload || !payload.player || !payload.player.isMe) return;
    const d = payload.delta || {};
    if (typeof d.goals === 'number') bucket.totals.goals += d.goals;
    if (typeof d.saves === 'number') bucket.totals.saves += d.saves;
  }

  function applyPlayerDemolished(bucket, payload) {
    if (payload && payload.attacker && payload.attacker.isMe) {
      bucket.totals.demos++;
    }
  }

  const MODIFIER_MAP = {
    isAerialGoal:    'aerial',
    isBicycleGoal:   'bicycle',
    isLongGoal:      'longGoal',
    isOvertimeGoal:  'overtime',
    isHatTrickGoal:  'hatTrick',
    isFlipResetGoal: 'flipReset',
    isBackwardsGoal: 'backwards',
    isTurtleGoal:    'turtle',
    isPoolShot:      'poolShot',
  };

  function applyGoalScored(bucket, payload) {
    if (!payload || !payload.scorer || !payload.scorer.isMe) return;
    const mods = payload.modifiers || {};
    for (const flag in MODIFIER_MAP) {
      if (mods[flag]) bucket.modifiers[MODIFIER_MAP[flag]]++;
    }
  }

  const Reducers = {
    emptyBucket,
    applyMatchEnded,
    applyPlayerScoreChanged,
    applyPlayerDemolished,
    applyGoalScored,
  };

  root.SessionTrackerReducers = Reducers;

  // SDK / DOM layer (skipped in tests)

  if (typeof RLT === 'undefined') return;

  // SDK registration and rendering live below; added in later tasks.
})(typeof window !== 'undefined' ? window : globalThis);
