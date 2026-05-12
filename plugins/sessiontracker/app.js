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

  function applyFastestShot(bucket, payload) {
    const s = payload && payload.speed;
    if (typeof s !== 'number' || !isFinite(s)) return;
    if (bucket.ball.fastestKmh === null || s > bucket.ball.fastestKmh) {
      bucket.ball.fastestKmh = s;
    }
  }

  function applyCrossbarHit(bucket, payload) {
    if (!payload) return;
    bucket.crossbar.hits++;
    const impact = payload.impactForce;
    const speed  = payload.ballSpeed;
    if (typeof impact !== 'number' || !isFinite(impact)) return;
    if (bucket.crossbar.hardest && impact <= bucket.crossbar.hardest.impact) return;
    const src = payload.ballLastTouch && payload.ballLastTouch.player;
    const player = src ? {
      name:  src.name || '',
      team:  typeof src.team === 'number' ? src.team : null,
      isMe:  !!src.isMe,
    } : null;
    bucket.crossbar.hardest = {
      impact,
      speed: typeof speed === 'number' ? speed : null,
      player,
      at: new Date().toISOString(),
    };
  }

  const RANKED_MODES = ['1v1', '2v2', '3v3'];

  function applyMmr(bucket, mode, payload) {
    const pls = payload && payload.playlists;
    if (!pls) return;
    if (RANKED_MODES.indexOf(mode) >= 0) {
      const row = pls[mode];
      if (row && typeof row.mmr === 'number') {
        const slot = bucket.mmr.ranked[mode];
        if (!slot) {
          bucket.mmr.ranked[mode] = { start: row.mmr, current: row.mmr };
        } else {
          slot.current = row.mmr;
        }
      }
    }
    const casual = pls.casual;
    if (casual && typeof casual.mmr === 'number') {
      if (!bucket.mmr.casual) {
        bucket.mmr.casual = { start: casual.mmr, current: casual.mmr };
      } else {
        bucket.mmr.casual.current = casual.mmr;
      }
    }
  }

  const Reducers = {
    emptyBucket,
    applyMatchEnded,
    applyPlayerScoreChanged,
    applyPlayerDemolished,
    applyGoalScored,
    applyFastestShot,
    applyCrossbarHit,
    applyMmr,
    RANKED_MODES,
  };

  root.SessionTrackerReducers = Reducers;

  // SDK / DOM layer (skipped in tests)

  if (typeof RLT === 'undefined') return;

  // SDK registration and rendering live below; added in later tasks.
})(typeof window !== 'undefined' ? window : globalThis);
