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

  function modeFromRoster(n) {
    if (n === 2) return '1v1';
    if (n === 4) return '2v2';
    if (n === 6) return '3v3';
    return 'other';
  }

  function currentStreak(last) {
    if (!Array.isArray(last) || last.length === 0) return null;
    const kind = last[last.length - 1];
    let count = 1;
    for (let i = last.length - 2; i >= 0; i--) {
      if (last[i] === kind) count++;
      else break;
    }
    if (count < 2) return null;
    return { kind, count };
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
    modeFromRoster,
    currentStreak,
  };

  root.SessionTrackerReducers = Reducers;

  // SDK / DOM layer (skipped in tests)

  if (typeof RLT === 'undefined') return;

  const STORE_KEY = 'session';
  const isOverlay  = RLT.isOverlay;
  const isSettings = RLT.isSettingsView;

  let bucket = null;
  let currentMode = null;
  let myTeam = null;
  let saveTimer = null;

  function save() {
    if (saveTimer) return;
    saveTimer = setTimeout(() => {
      saveTimer = null;
      RLT.store.set(STORE_KEY, bucket);
    }, 50);
  }

  function snapshotMyTeam() {
    const me = RLT.match.current && RLT.match.current.me;
    if (me && (me.team === 0 || me.team === 1)) myTeam = me.team;
  }

  function rosterSize() {
    const cur = RLT.match.current;
    return cur && Array.isArray(cur.players) ? cur.players.length : 0;
  }

  async function fetchMmr() {
    const b = bucket;
    const mode = currentMode;
    if (!b) return;
    try {
      const r = await fetch('/api/mmr');
      if (!r.ok) {
        console.warn('[sessiontracker] /api/mmr returned', r.status);
        return;
      }
      const j = await r.json();
      applyMmr(b, mode, j);
      save();
      scheduleRender();
    } catch (e) {
      console.warn('[sessiontracker] /api/mmr fetch failed:', e);
    }
  }

  function resolveBootID() {
    return new Promise((resolve) => {
      let resolved = false;
      const off = RLT.on('_BootId', (p) => {
        if (resolved) return;
        resolved = true; off();
        resolve(p && p.bootId);
      });
      setTimeout(async () => {
        if (resolved) return;
        try {
          const r = await fetch('/api/boot-id');
          const j = await r.json();
          if (resolved) return;
          resolved = true; off();
          resolve(j && j.bootId);
        } catch (_) {
          if (resolved) return;
          resolved = true; off();
          resolve(null);
        }
      }, 2000);
    });
  }

  // Render is wired in later tasks. For now stub it.
  function scheduleRender() { /* filled in Task 15 */ }

  RLT.plugin.register({
    async ready() {
      bucket = (await RLT.store.get(STORE_KEY)) || null;
      const liveBootId = await resolveBootID();
      if (!liveBootId) {
        if (!bucket) bucket = emptyBucket('');
      } else if (!bucket || bucket.bootId !== liveBootId) {
        bucket = emptyBucket(liveBootId);
        await RLT.store.set(STORE_KEY, bucket);
      }
      snapshotMyTeam();
      mountView();
    },

    events: {
      _BootId(p) {
        const id = p && p.bootId;
        if (id && bucket && bucket.bootId !== id) {
          bucket = emptyBucket(id);
          save(); scheduleRender();
        }
      },

      _MatchState(p) {
        snapshotMyTeam();
        if (p && p.phase === 'countdown') {
          const next = modeFromRoster(rosterSize());
          currentMode = next;
          if (RANKED_MODES.includes(next)) fetchMmr();
        } else if (p && p.phase === 'live' && currentMode === null) {
          // Fallback: countdown was missed (e.g. mid-match reconnect).
          const next = modeFromRoster(rosterSize());
          currentMode = next;
          if (RANKED_MODES.includes(next)) fetchMmr();
        }
      },

      _PlayerScoreChanged(p) {
        if (!bucket) return;
        snapshotMyTeam();
        applyPlayerScoreChanged(bucket, p);
        save(); scheduleRender();
      },

      _PlayerDemolished(p) {
        if (!bucket) return;
        applyPlayerDemolished(bucket, p);
        save(); scheduleRender();
      },

      _GoalScored(p) {
        if (!bucket) return;
        applyGoalScored(bucket, p);
        save(); scheduleRender();
      },

      _FastestShotOfMatch(p) {
        if (!bucket) return;
        applyFastestShot(bucket, p);
        save(); scheduleRender();
      },

      _CrossbarHit(p) {
        if (!bucket) return;
        applyCrossbarHit(bucket, p);
        save(); scheduleRender();
      },

      _MatchEnded(p) {
        if (!bucket) return;
        snapshotMyTeam();
        applyMatchEnded(bucket, p, myTeam);
        save(); scheduleRender();
        if (RANKED_MODES.includes(currentMode)) {
          fetchMmr();
        }
      },
    },
  });

  function mountView() {
    // Filled in Task 15.
  }
})(typeof window !== 'undefined' ? window : globalThis);
