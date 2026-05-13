// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  // Pure data layer (testable; no DOM, no SDK)

  function emptyModifiers() {
    // ownGoal is tracked alongside the positive goal modifiers so
    // the modifiers block is the single home for "things that
    // happened during a goal." The renderer flags it visually as
    // negative; the data layer treats it like any other modifier.
    return {
      aerial: 0, bicycle: 0, longGoal: 0, overtime: 0,
      hatTrick: 0, flipReset: 0, backwards: 0, turtle: 0, poolShot: 0,
      ownGoal: 0,
    };
  }

  function emptyMatch() {
    return {
      result: null,
      goals: 0, saves: 0, demos: 0,
      boost: 0, pickups: 0,
      modifiers: emptyModifiers(),
    };
  }

  function emptyBucket(bootId) {
    return {
      bootId: bootId || '',
      startedAt: new Date().toISOString(),
      // lastTalliedGuid dedupes the W/L tally so the same match can't
      // count twice. Both _MatchEnded (the wire event) and a phase=
      // ended state transition can call applyMatchEnded — we want the
      // first one to win so the user's record is correct even if one
      // of the two signals never arrives or arrives late.
      lastTalliedGuid: '',
      // Tracks which match the per-match block currently describes.
      // Used by applyMatchStateForReset to distinguish a new-match
      // countdown from a post-goal kickoff countdown — same matchGuid
      // across consecutive countdowns means we're still inside one
      // match. null until the first countdown is observed.
      lastMatchGuid: null,
      results: { wins: 0, losses: 0, last: [] },
      // boost is the total boost amount consumed (active spend, summed
      // percent); pickups is the count of _BoostPickup events (one per
      // pad pickup, not the cumulative percent). Own goals are tracked
      // in modifiers, not here, since they're a property of a goal
      // event rather than a per-player counter.
      totals:  { goals: 0, saves: 0, demos: 0, boost: 0, pickups: 0 },
      // Per-match counters. Mirror the totals shape so render code can
      // reuse the same field names. result is null until applyMatchEnded
      // stamps a 'win' or 'loss'; resetMatch (called on _MatchState
      // countdown) zeros the counters between matches so the overlay
      // shows the live numbers for the current match, not a cumulative
      // mash of every match in the session.
      match: emptyMatch(),
      modifiers: emptyModifiers(),
      // Two ball-speed metrics with different scopes:
      //   fastestKmh         — match-wide top ball speed (any player).
      //                        Drives the FASTEST display row, sourced
      //                        from _FastestShotOfMatch.
      //   myFastestHitKmh    — my hardest hit, i.e. the fastest I've
      //                        made the ball go via a touch. Drives the
      //                        MY HIT display, sourced from _BallHit
      //                        filtered to my touches.
      // fastestGoalKmh — fastest goal I've scored, from
      // _GoalScored's goalSpeed (filtered to my scores, excluding
      // own goals). Distinct from fastestKmh, which is the lobby's
      // top shot, and myFastestHitKmh, which is my hardest touch
      // (not necessarily resulting in a goal).
      ball: { fastestKmh: null, myFastestHitKmh: null, fastestGoalKmh: null },
      crossbar: { hits: 0, hardest: null },
      mmr: { ranked: {}, casual: null },
    };
  }

  function resetMatch(bucket) {
    bucket.match = emptyMatch();
  }

  // _MatchStarted is the authoritative "new match has begun" signal
  // from the backend — fires exactly once per matchGuid on the first
  // live-ish transition. Reset the per-match block and stamp the
  // guid so the forfeit-loss handler can tell whether we ever tallied
  // it. Empty guid is treated as a real identity (offline / private
  // play) so we still get a clean reset.
  function applyMatchStarted(bucket, payload) {
    if (!bucket || !payload) return;
    const guid = typeof payload.matchGuid === 'string' ? payload.matchGuid : '';
    bucket.lastMatchGuid = guid;
    resetMatch(bucket);
  }

  // applyMatchAbandoned tallies a forfeit loss when MatchDestroyed
  // fires without _MatchEnded having tallied first. RL gives the
  // leaver a forfeit-loss penalty either way, so we follow suit. No-op
  // if we never observed this match (lastMatchGuid empty) or the
  // result was already tallied. myTeam null means we never figured out
  // which team we're on — bail rather than guess.
  function applyMatchAbandoned(bucket, myTeam) {
    if (!bucket) return;
    if (myTeam !== 0 && myTeam !== 1) return;
    const guid = bucket.lastMatchGuid || '';
    if (!guid) return;
    if (bucket.lastTalliedGuid === guid) return;
    bucket.results.losses++;
    bucket.results.last.push('loss');
    if (bucket.results.last.length > 10) bucket.results.last.shift();
    bucket.match.result = 'loss';
    bucket.lastTalliedGuid = guid;
  }

  function applyMatchEnded(bucket, payload, myTeam) {
    if (myTeam !== 0 && myTeam !== 1) return;
    if (!payload) return;
    // Guid dedupe: the same match can be reported through both the
    // _MatchEnded wire event and a phase=ended state transition; only
    // the first call should tally. Empty guid is treated as "no
    // identity" and always counts (offline play, fixture replay, etc.).
    const guid = typeof payload.matchGuid === 'string' ? payload.matchGuid : '';
    if (guid && bucket.lastTalliedGuid === guid) return;

    let winner = payload.winnerTeamNum;
    if (winner !== 0 && winner !== 1) {
      // Winner missing — try to derive it from team scores. Ties or
      // missing scores stay ambiguous and we bail rather than guess.
      const sb = payload.scoreBlue;
      const so = payload.scoreOrange;
      if (typeof sb === 'number' && typeof so === 'number' && sb !== so) {
        winner = sb > so ? 0 : 1;
      } else {
        return;
      }
    }
    const result = winner === myTeam ? 'win' : 'loss';
    if (result === 'win') bucket.results.wins++;
    else bucket.results.losses++;
    bucket.results.last.push(result);
    if (bucket.results.last.length > 10) bucket.results.last.shift();
    // Stamp the result on the per-match block so the overlay can show
    // the just-finished match's outcome alongside its live counters
    // (which freeze here until resetMatch runs at the next countdown).
    bucket.match.result = result;
    if (guid) bucket.lastTalliedGuid = guid;
  }

  function applyPlayerScoreChanged(bucket, payload) {
    if (!payload || !payload.player || !payload.player.isMe) return;
    const d = payload.delta || {};
    if (typeof d.goals === 'number') {
      bucket.totals.goals += d.goals;
      bucket.match.goals += d.goals;
    }
    if (typeof d.saves === 'number') {
      bucket.totals.saves += d.saves;
      bucket.match.saves += d.saves;
    }
  }

  function applyPlayerDemolished(bucket, payload) {
    if (payload && payload.attacker && payload.attacker.isMe) {
      bucket.totals.demos++;
      bucket.match.demos++;
    }
  }

  function applyBoostConsumed(bucket, payload) {
    if (!payload || !payload.player || !payload.player.isMe) return;
    const d = payload.delta;
    if (typeof d !== 'number' || d <= 0) return;
    bucket.totals.boost += d;
    bucket.match.boost += d;
  }

  function applyBoostPickup(bucket, payload) {
    if (!payload || !payload.player || !payload.player.isMe) return;
    const d = payload.delta;
    if (typeof d !== 'number' || d <= 0) return;
    bucket.totals.pickups += 1;
    bucket.match.pickups += 1;
  }

  function applyOwnGoal(bucket, payload) {
    if (!payload || !payload.deflector || !payload.deflector.isMe) return;
    bucket.modifiers.ownGoal++;
    bucket.match.modifiers.ownGoal++;
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
      if (mods[flag]) {
        const k = MODIFIER_MAP[flag];
        bucket.modifiers[k]++;
        bucket.match.modifiers[k]++;
      }
    }
  }

  // applyMatchTopSpeed: match-wide top ball speed across all players.
  // Sourced from _FastestShotOfMatch, which the toolkit only publishes
  // when a new match-wide max is observed — so the receiver just
  // remembers the latest. Defensive ratchet so an out-of-order replay
  // can't lower the recorded max.
  function applyMatchTopSpeed(bucket, payload) {
    const s = payload && payload.speed;
    if (typeof s !== 'number' || !isFinite(s)) return;
    if (bucket.ball.fastestKmh === null || s > bucket.ball.fastestKmh) {
      bucket.ball.fastestKmh = s;
    }
  }

  // applyMyHit: my hardest ball touch. Sourced from _BallHit filtered
  // to my touches (players[0] is the toucher). Tracks postHitSpeed —
  // the speed of the ball after I hit it — as the personal record.
  function applyMyHit(bucket, payload) {
    if (!payload) return;
    const players = payload.players;
    if (!Array.isArray(players) || players.length === 0) return;
    const toucher = players[0];
    if (!toucher || !toucher.isMe) return;
    const s = payload.postHitSpeed;
    if (typeof s !== 'number' || !isFinite(s)) return;
    if (bucket.ball.myFastestHitKmh === null || s > bucket.ball.myFastestHitKmh) {
      bucket.ball.myFastestHitKmh = s;
    }
  }

  // applyMyFastestGoal: from _GoalScored. Ratchets fastestGoalKmh
  // upward with goalSpeed for goals I scored, ignoring own goals
  // (an own goal is still a "me scoring" event in our reducers'
  // sense via scorer.isMe, but it shouldn't count toward a personal
  // fastest-goal record).
  function applyMyFastestGoal(bucket, payload) {
    if (!payload || !payload.scorer || !payload.scorer.isMe) return;
    if (payload.isOwnGoal === true) return;
    const s = payload.goalSpeed;
    if (typeof s !== 'number' || !isFinite(s)) return;
    if (bucket.ball.fastestGoalKmh === null || s > bucket.ball.fastestGoalKmh) {
      bucket.ball.fastestGoalKmh = s;
    }
  }

  function applyCrossbarHit(bucket, payload) {
    if (!payload) return;
    // Same me-only rule as applyFastestShot. The hits counter and the
    // hardest record are both about my play, not the lobby's. Without
    // this filter the overlay shows nine crossbars and credits another
    // player's hardest hit.
    const src = payload.ballLastTouch && payload.ballLastTouch.player;
    if (!src || !src.isMe) return;
    bucket.crossbar.hits++;
    const impact = payload.impactForce;
    const speed  = payload.ballSpeed;
    if (typeof impact !== 'number' || !isFinite(impact)) return;
    if (bucket.crossbar.hardest && impact <= bucket.crossbar.hardest.impact) return;
    bucket.crossbar.hardest = {
      impact,
      speed: typeof speed === 'number' ? speed : null,
      player: {
        name: src.name || '',
        team: typeof src.team === 'number' ? src.team : null,
        isMe: true,
      },
      at: new Date().toISOString(),
    };
  }

  const RANKED_MODES = ['1v1', '2v2', '3v3'];

  function applyMmr(bucket, mode, payload) {
    const pls = payload && payload.playlists;
    if (!pls) return;
    // Seed every ranked playlist that came back, not just the active
    // mode. Without this, the MMR row is empty until the user queues
    // into the right playlist and a match starts — which can be minutes
    // after launch. With it, the overlay shows current MMR for every
    // ranked mode the API knows about; deltas start at 0 and shift as
    // matches end. The active mode (passed in) is still tracked the
    // same way; the only difference is that we also seed the others.
    for (let i = 0; i < RANKED_MODES.length; i++) {
      const m = RANKED_MODES[i];
      const row = pls[m];
      if (!row || typeof row.mmr !== 'number') continue;
      const slot = bucket.mmr.ranked[m];
      if (!slot) {
        bucket.mmr.ranked[m] = { start: row.mmr, current: row.mmr };
      } else if (m === mode) {
        // Only the active mode advances `current`; idle modes keep
        // whatever start/current pair they were seeded with so a
        // background MMR refresh doesn't silently rewrite their
        // session delta.
        slot.current = row.mmr;
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
    resetMatch,
    applyMatchStarted,
    applyMatchAbandoned,
    applyMatchEnded,
    applyPlayerScoreChanged,
    applyPlayerDemolished,
    applyBoostConsumed,
    applyBoostPickup,
    applyOwnGoal,
    applyGoalScored,
    applyMatchTopSpeed,
    applyMyHit,
    applyMyFastestGoal,
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
  // myTeam caches the most recent team we observed for the local
  // player. Used as a fallback when _MatchEnded fires after the
  // roster has already been torn down (reconnect race, late SSE
  // delivery). The primary read is RLT.match.current.me.team at
  // _MatchEnded time; this cache is the safety net.
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

  // cacheMyTeamFromRef: opportunistic team capture from any event
  // payload that carries an enriched player ref. Private/offline
  // matches don't always populate RLT.match.current.me reliably
  // (no roster snapshots in some modes), so snapshotMyTeam from
  // _MatchState alone isn't enough. Every event with a .player or
  // .scorer or .attacker that's me also carries that player's
  // team — caching it here keeps myTeam fresh through the whole
  // match, including the frame _MatchEnded fires on.
  function cacheMyTeamFromRef(ref) {
    if (ref && ref.isMe && (ref.team === 0 || ref.team === 1)) {
      myTeam = ref.team;
    }
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

  function fmtSpeed(v)  { return (v == null) ? '—' : v.toFixed(1) + ' km/h'; }
  function fmtImpact(v) { return (v == null) ? '—' : Math.round(v) + ' N'; }
  function fmtDuration(startISO) {
    const ms = Date.now() - new Date(startISO).getTime();
    if (!isFinite(ms) || ms < 0) return '00:00';
    const total = Math.floor(ms / 1000);
    const h = Math.floor(total / 3600);
    const m = Math.floor((total % 3600) / 60);
    if (h > 0) return h + 'h ' + String(m).padStart(2, '0') + 'm';
    const s = total % 60;
    return String(m).padStart(2, '0') + ':' + String(s).padStart(2, '0');
  }

  function deltaText(slot) {
    if (!slot) return '—';
    const d = slot.current - slot.start;
    // Always render the sign so a zero delta visually reads as "no
    // change" (±0) rather than a bare "0" sitting next to the
    // parenthesized current MMR — that combination looked like a
    // malformed stat.
    const sign = d > 0 ? '+' : (d < 0 ? '−' : '±');
    const abs = Math.abs(d);
    return sign + abs + ' (' + slot.current + ')';
  }
  function deltaClass(slot) {
    if (!slot) return 'st-mut';
    const d = slot.current - slot.start;
    if (d > 0) return 'st-pos';
    if (d < 0) return 'st-neg';
    return 'st-mut';
  }

  function topModifiers(mods) {
    const entries = Object.keys(mods)
      .map((k) => ({ k, v: mods[k] }))
      .filter((e) => e.v > 0)
      .sort((a, b) => (b.v - a.v) || a.k.localeCompare(b.k));
    const shown = entries.slice(0, 5);
    const extra = entries.length - shown.length;
    return { shown, extra };
  }

  // listModifiers: full sorted list of fired modifiers for the
  // cockpit overlay's Goal Modifiers section. Sort by count desc,
  // tie-break alphabetic, with ownGoal pinned to the bottom so the
  // negative entry doesn't bury the positives.
  function listModifiers(mods) {
    return Object.keys(mods)
      .map((k) => ({ k, v: mods[k] }))
      .filter((e) => e.v > 0)
      .sort((a, b) => {
        if (a.k === 'ownGoal' && b.k !== 'ownGoal') return 1;
        if (b.k === 'ownGoal' && a.k !== 'ownGoal') return -1;
        return (b.v - a.v) || a.k.localeCompare(b.k);
      });
  }

  const MOD_LABEL = {
    aerial: 'AERIAL', bicycle: 'BICYCLE', longGoal: 'LONG', overtime: 'OT',
    hatTrick: 'HAT TRICK', flipReset: 'FLIP RESET', backwards: 'BACKWARDS',
    turtle: 'TURTLE', poolShot: 'POOL', ownGoal: 'OWN GOAL',
  };

  let settings = { showStreak: true };

  async function loadSettings() {
    try {
      const s = await RLT.store.get('settings');
      if (s && typeof s.showStreak === 'boolean') settings.showStreak = s.showStreak;
    } catch (_) { /* defaults are fine */ }
  }

  function esc(s)  { return RLT.ui.esc(String(s)); }
  function escA(s) { return RLT.ui.escAttr(String(s)); }

  // Cockpit overlay: match block dominant on top, session strip
  // below as telemetry rows, with a dedicated Goal Modifiers
  // section at the bottom split into Match / Session columns.
  // All class names live in the st-c-* namespace so they can't
  // collide with the dashboard's st-* styles.
  function renderOverlay(root) {
    const b = bucket || emptyBucket('');
    const m = b.match || emptyMatch();

    const streak = settings.showStreak ? currentStreak(b.results.last) : null;
    const streakLabel = streak ? (streak.kind === 'win' ? 'W' : 'L') + streak.count : '';
    const ticksHtml = b.results.last.map((r) =>
      '<span class="st-c-tick st-c-tick-' + escA(r) + '"></span>'
    ).join('');

    const mode = currentMode || '—';
    const dur  = fmtDuration(b.startedAt);

    // Match block — big stat cells. A zero count dims so the eye
    // lands on values that actually moved. No win/loss badge here:
    // the per-match outcome is already encoded in the record bar
    // (latest tick + streak label), and rendering a "W" inside
    // "LIVE MATCH" reads as the live match having an outcome —
    // misleading once the next match's countdown begins.
    const cellCls = (v) => v > 0 ? 'st-c-stat-v' : 'st-c-stat-v zero';
    const matchGrid =
      '<div class="st-c-stat"><span class="' + cellCls(m.goals)   + '">' + m.goals   + '</span><span class="st-c-stat-l">Goals</span></div>' +
      '<div class="st-c-stat"><span class="' + cellCls(m.saves)   + '">' + m.saves   + '</span><span class="st-c-stat-l">Saves</span></div>' +
      '<div class="st-c-stat"><span class="' + cellCls(m.demos)   + '">' + m.demos   + '</span><span class="st-c-stat-l">Demos</span></div>' +
      '<div class="st-c-stat"><span class="' + cellCls(m.boost)   + '">' + m.boost   + '</span><span class="st-c-stat-l">Boost</span></div>' +
      '<div class="st-c-stat"><span class="' + cellCls(m.pickups) + '">' + m.pickups + '</span><span class="st-c-stat-l">Pickups</span></div>';

    // Session telemetry strip — three rows: Session totals, Ball
    // Speed, MMR. Each row uses the same "label · chips" rhythm.
    const sesRow =
      '<div class="st-c-srow">' +
        '<span class="st-c-srow-l">Session</span>' +
        '<span class="st-c-srow-v">' +
          '<span class="st-c-pair"><small>Goals</small><b>'   + b.totals.goals   + '</b></span>' +
          '<span class="st-c-pair"><small>Saves</small><b>'   + b.totals.saves   + '</b></span>' +
          '<span class="st-c-pair"><small>Demos</small><b>'   + b.totals.demos   + '</b></span>' +
          '<span class="st-c-pair"><small>Boost</small><b>'   + b.totals.boost   + '</b></span>' +
          '<span class="st-c-pair"><small>Pickups</small><b>' + b.totals.pickups + '</b></span>' +
        '</span>' +
      '</div>';

    const hardest = b.crossbar.hardest;
    const hits = b.crossbar.hits;
    const crossbarPair = hardest
      ? '<span class="st-c-pair"><small>Crossbar</small><b>' + hits + '</b><small class="dim">· ' + esc(fmtImpact(hardest.impact)) + ' · ' + esc(fmtSpeed(hardest.speed)) + '</small></span>'
      : '<span class="st-c-pair"><small>Crossbar</small><b' + (hits ? '' : ' class="mut"') + '>' + (hits || '—') + '</b></span>';
    const ballRow =
      '<div class="st-c-srow">' +
        '<span class="st-c-srow-l">Ball Speed</span>' +
        '<span class="st-c-srow-v">' +
          '<span class="st-c-pair"><small>Goal</small><b' + (b.ball.fastestGoalKmh ? '' : ' class="mut"') + '>' + esc(fmtSpeed(b.ball.fastestGoalKmh)) + '</b></span>' +
          '<span class="st-c-pair"><small>Shot</small><b' + (b.ball.myFastestHitKmh ? '' : ' class="mut"') + '>' + esc(fmtSpeed(b.ball.myFastestHitKmh)) + '</b></span>' +
          crossbarPair +
        '</span>' +
      '</div>';

    // MMR — show 1v1 and casual as two slots, no "ranked" label
    // (we can't actually tell whether the player is queueing ranked
    // or casual at any given time, so the label was misleading).
    const rankedMode = (currentMode && RANKED_MODES.indexOf(currentMode) >= 0)
      ? currentMode
      : (RANKED_MODES.find((mm) => b.mmr.ranked[mm]) || '2v2');
    const rankedSlot = b.mmr.ranked[rankedMode] || null;
    const casualSlot = b.mmr.casual;
    const mmrRow =
      '<div class="st-c-srow">' +
        '<span class="st-c-srow-l">MMR</span>' +
        '<span class="st-c-srow-v">' +
          '<span class="st-c-pair"><small>' + esc(rankedMode) + '</small>' +
            '<b' + (rankedSlot ? '' : ' class="mut"') + '>' + (rankedSlot ? rankedSlot.current : '—') + '</b>' +
            '<small class="' + deltaClass(rankedSlot) + '">' + esc(deltaText(rankedSlot)) + '</small></span>' +
          '<span class="st-c-pair"><small>Casual</small>' +
            '<b' + (casualSlot ? '' : ' class="mut"') + '>' + (casualSlot ? casualSlot.current : '—') + '</b>' +
            '<small class="' + deltaClass(casualSlot) + '">' + esc(deltaText(casualSlot)) + '</small></span>' +
        '</span>' +
      '</div>';

    // Goal modifiers — two-column block at the bottom. Each column
    // lists fired modifiers in descending count, with ownGoal pinned
    // to the bottom and colored magenta to read as a negative event.
    function renderModColumn(mods, opts) {
      const items = listModifiers(mods);
      if (items.length === 0) {
        return '<div class="st-c-mod-list st-c-mod-empty">—</div>';
      }
      return '<div class="st-c-mod-list">' +
        items.map((e) => {
          const cls = e.k === 'ownGoal' ? 'st-c-mod-item neg' : 'st-c-mod-item';
          return '<div class="' + cls + '">' +
            '<span class="st-c-mod-n">' + esc(MOD_LABEL[e.k]) + '</span>' +
            '<span class="st-c-mod-v">' + e.v + '</span>' +
          '</div>';
        }).join('') +
      '</div>';
    }
    const modsBlock =
      '<div class="st-c-mods">' +
        '<div class="st-c-mods-h">Goal Modifiers</div>' +
        '<div class="st-c-mods-cols">' +
          '<div class="st-c-mod-col live">' +
            '<div class="st-c-mod-col-h">Match</div>' +
            renderModColumn(m.modifiers) +
          '</div>' +
          '<div class="st-c-mod-col">' +
            '<div class="st-c-mod-col-h">Session</div>' +
            renderModColumn(b.modifiers) +
          '</div>' +
        '</div>' +
      '</div>';

    root.innerHTML =
      '<div class="st-c-card">' +
        '<div class="st-c-head">' +
          '<span class="st-c-title">Session Tracker</span>' +
          '<span class="st-c-meta"><b>' + esc(mode) + '</b> · Started ' + esc(dur) + ' ago</span>' +
        '</div>' +
        '<div class="st-c-record">' +
          '<span class="st-c-wl">' + b.results.wins + '<span class="dash">–</span>' + b.results.losses + '</span>' +
          (ticksHtml ? '<span class="st-c-ticks">' + ticksHtml + '</span>' : '<span></span>') +
          (streakLabel ? '<span class="st-c-streak">' + esc(streakLabel) + '</span>' : '<span></span>') +
        '</div>' +
        '<div class="st-c-match">' +
          '<div class="st-c-match-head">' +
            '<span class="st-c-match-tag">Live Match</span>' +
          '</div>' +
          '<div class="st-c-match-grid">' + matchGrid + '</div>' +
        '</div>' +
        '<div class="st-c-strip">' +
          sesRow + ballRow + mmrRow +
        '</div>' +
        modsBlock +
      '</div>';
  }

  function renderDashboard(root) {
    const b = bucket || emptyBucket('');
    const streak = currentStreak(b.results.last);
    const streakLabel = streak ? (streak.kind === 'win' ? 'STREAK W' : 'STREAK L') + streak.count : '';

    const ticks = b.results.last.map((r) =>
      '<span class="st-tick st-tick-' + escA(r) + '"></span>'
    ).join('');

    const startedTime = new Date(b.startedAt).toLocaleTimeString('en', {
      hour12: false, hour: '2-digit', minute: '2-digit',
    });

    const hardest = b.crossbar.hardest;
    let hardestBlock;
    if (!hardest) {
      hardestBlock = '<div class="st-mut">No crossbar hits yet</div>';
    } else {
      const team = hardest.player ? (hardest.player.team === 0 ? ' (Blue)' : hardest.player.team === 1 ? ' (Orange)' : '') : '';
      const when = hardest.at ? new Date(hardest.at).toLocaleTimeString('en', { hour12: false, hour: '2-digit', minute: '2-digit' }) : '';
      const playerName = hardest.player ? (hardest.player.name || '?') : '?';
      hardestBlock =
        '<div>Hardest impact this session:</div>' +
        '<div class="st-hardest-line"><span class="st-val">' + fmtImpact(hardest.impact) + '</span>' +
          ' · <span class="st-val">' + fmtSpeed(hardest.speed) + '</span>' +
          ' — <span class="st-name">' + esc(playerName) + esc(team) + '</span>' +
          (when ? ' — <span class="st-mut">' + esc(when) + '</span>' : '') +
        '</div>' +
        '<div class="st-mut">Total hits: ' + b.crossbar.hits + '</div>';
    }

    const mmrRows = [];
    for (const mode of RANKED_MODES) {
      const slot = b.mmr.ranked[mode];
      if (!slot) continue;
      mmrRows.push(
        '<tr><td class="st-lbl">' + mode + '</td>' +
        '<td><span class="' + deltaClass(slot) + '">' + esc(deltaText(slot)) + '</span></td></tr>'
      );
    }
    if (b.mmr.casual) {
      mmrRows.push(
        '<tr><td class="st-lbl">CASUAL</td>' +
        '<td><span class="' + deltaClass(b.mmr.casual) + '">' + esc(deltaText(b.mmr.casual)) + '</span></td></tr>'
      );
    }

    const modOrder = Object.keys(MOD_LABEL).slice().sort((a, b2) => {
      const da = b.modifiers[b2] - b.modifiers[a];
      if (da !== 0) return da;
      return a.localeCompare(b2);
    });
    const modCells = modOrder.map((k) => {
      const v = b.modifiers[k];
      const cls = v > 0 ? 'st-val' : 'st-mut';
      return '<div class="st-mod-cell"><span class="st-lbl">' + esc(MOD_LABEL[k]) + '</span> <span class="' + cls + '">' + v + '</span></div>';
    }).join('');

    const totalsCls = (v) => v > 0 ? 'st-val' : 'st-mut';

    root.innerHTML =
      '<div class="st-dash">' +
        '<div class="st-dash-h">' +
          '<span class="st-h-l">SESSION TRACKER</span>' +
          '<span class="st-h-r"><span class="st-mut">started ' + esc(startedTime) + ' · ' + esc(fmtDuration(b.startedAt)) + ' elapsed</span></span>' +
        '</div>' +

        '<div class="st-dash-grid">' +
          '<div class="st-card st-card-record">' +
            '<div class="st-card-h">RECORD</div>' +
            '<div class="st-record-big">' + b.results.wins + ' – ' + b.results.losses + '</div>' +
            '<div class="st-record-lbl"><span class="st-lbl">WINS</span> <span class="st-lbl">LOSSES</span></div>' +
            '<div class="st-row st-ticks">' + (ticks || '<span class="st-mut">no matches yet</span>') + '</div>' +
            (streakLabel ? '<div class="st-mut">' + esc(streakLabel) + '</div>' : '') +
          '</div>' +

          '<div class="st-card st-card-stats">' +
            '<div class="st-card-h">STATS</div>' +
            '<div class="st-stats-big">' +
              '<div><div class="' + totalsCls(b.totals.goals) + ' st-num">' + b.totals.goals + '</div><div class="st-lbl">GOALS</div></div>' +
              '<div><div class="' + totalsCls(b.totals.saves) + ' st-num">' + b.totals.saves + '</div><div class="st-lbl">SAVES</div></div>' +
              '<div><div class="' + totalsCls(b.totals.demos) + ' st-num">' + b.totals.demos + '</div><div class="st-lbl">DEMOS</div></div>' +
            '</div>' +
            '<div class="st-stats-info">' +
              '<div><span class="' + (b.ball.fastestKmh ? 'st-val' : 'st-mut') + '">' + fmtSpeed(b.ball.fastestKmh) + '</span> <span class="st-lbl">FASTEST BALL</span></div>' +
              '<div>' +
                '<span class="' + (hardest ? 'st-val' : 'st-mut') + '">' + (hardest ? fmtImpact(hardest.impact) + ' · ' + fmtSpeed(hardest.speed) : '—') + '</span>' +
                ' <span class="st-lbl">HARDEST CROSSBAR</span>' +
                (hardest && hardest.player ? '<div class="st-mut">· by ' + esc(hardest.player.name || '?') + '</div>' : '') +
              '</div>' +
            '</div>' +
          '</div>' +
        '</div>' +

        (mmrRows.length > 0 ? (
          '<div class="st-card">' +
            '<div class="st-card-h">MMR</div>' +
            '<table class="st-table"><tbody>' + mmrRows.join('') + '</tbody></table>' +
          '</div>'
        ) : '') +

        '<div class="st-card">' +
          '<div class="st-card-h">GOAL MODIFIERS</div>' +
          '<div class="st-mods-grid">' + modCells + '</div>' +
        '</div>' +

        '<div class="st-card">' +
          '<div class="st-card-h">CROSSBAR</div>' +
          hardestBlock +
        '</div>' +
      '</div>';
  }
  function renderSettings(root) {
    root.innerHTML =
      '<div class="st-set">' +
        '<div class="st-set-h">SESSION TRACKER · SETTINGS</div>' +

        '<div class="st-card">' +
          '<div class="st-card-h">DISPLAY</div>' +
          '<label class="st-toggle">' +
            '<input id="st-toggle-streak" type="checkbox"' + (settings.showStreak ? ' checked' : '') + ' />' +
            '<span class="st-toggle-track"><span class="st-toggle-thumb"></span></span>' +
            '<span class="st-toggle-text">' +
              '<span class="st-toggle-title">Show streak chip on overlay</span>' +
              '<span class="st-toggle-hint">Hides the W2/L3 indicator next to the score.</span>' +
            '</span>' +
          '</label>' +
        '</div>' +

        '<div class="st-card">' +
          '<div class="st-card-h">SESSION</div>' +
          '<div class="st-set-row">' +
            '<div class="st-set-row-text">' +
              '<div class="st-set-row-title">Reset current session</div>' +
              '<div class="st-set-row-hint">Clears wins, losses, and all stats. MMR starting values are re-seeded on the next match.</div>' +
            '</div>' +
            '<button id="st-reset" class="st-danger">RESET</button>' +
          '</div>' +
        '</div>' +
      '</div>';

    const toggle = document.getElementById('st-toggle-streak');
    toggle.addEventListener('change', async () => {
      settings.showStreak = toggle.checked;
      try { await RLT.store.set('settings', { showStreak: settings.showStreak }); } catch (_) {}
    });

    const reset = document.getElementById('st-reset');
    reset.addEventListener('click', async () => {
      if (!window.confirm('Reset session?')) return;
      bucket = emptyBucket(bucket ? bucket.bootId : '');
      try { await RLT.store.set(STORE_KEY, bucket); } catch (_) {}
      currentMode = null;
      scheduleRender();
    });
  }

  let elapsedTimer = null;

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
      // Backfill fields added in newer plugin versions so render code
      // and the dedupe path can assume they're present.
      if (bucket && !bucket.match) {
        bucket.match = { result: null, goals: 0, saves: 0, demos: 0, boost: 0, ownGoals: 0 };
      }
      if (bucket && bucket.match && bucket.match.boost === undefined) {
        bucket.match.boost = 0;
      }
      if (bucket && bucket.match && bucket.match.pickups === undefined) {
        bucket.match.pickups = 0;
      }
      if (bucket && bucket.match && !bucket.match.modifiers) {
        bucket.match.modifiers = emptyModifiers();
      }
      // ownGoal moved from totals/match into the modifiers blocks.
      // Fold any legacy counts forward, then drop the old keys so
      // saved state doesn't accumulate dead fields.
      if (bucket && bucket.modifiers && bucket.modifiers.ownGoal === undefined) {
        bucket.modifiers.ownGoal = 0;
      }
      if (bucket && bucket.match && bucket.match.modifiers && bucket.match.modifiers.ownGoal === undefined) {
        bucket.match.modifiers.ownGoal = 0;
      }
      if (bucket && bucket.totals && typeof bucket.totals.ownGoals === 'number') {
        bucket.modifiers.ownGoal += bucket.totals.ownGoals;
        delete bucket.totals.ownGoals;
      }
      if (bucket && bucket.match && typeof bucket.match.ownGoals === 'number') {
        bucket.match.modifiers.ownGoal += bucket.match.ownGoals;
        delete bucket.match.ownGoals;
      }
      if (bucket && bucket.totals && bucket.totals.boost === undefined) {
        bucket.totals.boost = 0;
      }
      if (bucket && bucket.totals && bucket.totals.pickups === undefined) {
        bucket.totals.pickups = 0;
      }
      if (bucket && typeof bucket.lastTalliedGuid !== 'string') {
        bucket.lastTalliedGuid = '';
      }
      if (bucket && bucket.lastMatchGuid === undefined) {
        bucket.lastMatchGuid = null;
      }
      if (bucket && bucket.ball && bucket.ball.myFastestHitKmh === undefined) {
        bucket.ball.myFastestHitKmh = null;
      }
      if (bucket && bucket.ball && bucket.ball.fastestGoalKmh === undefined) {
        bucket.ball.fastestGoalKmh = null;
      }
      snapshotMyTeam();
      mountView();
      // Pull MMR straight away so the row has data before the first
      // match. Without this the MMR bar stays empty until the next
      // countdown event fires, which can be minutes if the user just
      // launched into the menu. fetchMmr no-ops when bucket is null.
      fetchMmr();
    },

    events: {
      _BootId(p) {
        const id = p && p.bootId;
        if (id && bucket && bucket.bootId !== id) {
          bucket = emptyBucket(id);
          save(); scheduleRender();
        }
      },

      _MatchStarted(p) {
        if (bucket) {
          applyMatchStarted(bucket, p);
          save(); scheduleRender();
        }
        const next = modeFromRoster(rosterSize());
        currentMode = next;
        if (RANKED_MODES.includes(next)) fetchMmr();
      },

      _MatchAbandoned() {
        // Backend fires this synthetic when MatchDestroyed arrives
        // without a matching MatchEnded — user ragequit, disconnect,
        // server crash. RL itself counts these as a forfeit loss, so
        // we mirror that on the W/L bar. The backend dedupes the
        // event against the wire MatchEnded, so we don't need to.
        if (!bucket) return;
        snapshotMyTeam();
        const team = (typeof myTeam === 'number') ? myTeam : null;
        applyMatchAbandoned(bucket, team);
        save(); scheduleRender();
      },

      _MatchState(p) {
        snapshotMyTeam();
        if (p && p.phase === 'live' && currentMode === null) {
          // Fallback: countdown was missed (e.g. mid-match reconnect).
          const next = modeFromRoster(rosterSize());
          currentMode = next;
          if (RANKED_MODES.includes(next)) fetchMmr();
        } else if (p && (p.phase === 'ended' || p.phase === 'podium')) {
          // Belt-and-braces tally on phase=ended (and podium, which
          // sometimes lands without us seeing 'ended' if the user
          // forfeits or the connection blips). The reducer dedupes
          // by matchGuid, so calling this in addition to _MatchEnded
          // is safe — first signal wins. Catches the case where the
          // wire _MatchEnded event never reaches us, which the user
          // has reported.
          if (bucket && RLT.match && RLT.match.current) {
            const cur = RLT.match.current;
            const live = cur.me;
            const team = (live && (live.team === 0 || live.team === 1)) ? live.team : myTeam;
            const synth = {
              matchGuid: cur.guid || '',
              scoreBlue: cur.scoreBlue,
              scoreOrange: cur.scoreOrange,
            };
            applyMatchEnded(bucket, synth, team);
            save(); scheduleRender();
          }
        }
      },

      _PlayerScoreChanged(p) {
        if (!bucket) return;
        snapshotMyTeam();
        cacheMyTeamFromRef(p && p.player);
        applyPlayerScoreChanged(bucket, p);
        save(); scheduleRender();
      },

      _PlayerDemolished(p) {
        if (!bucket) return;
        cacheMyTeamFromRef(p && p.attacker);
        cacheMyTeamFromRef(p && p.victim);
        applyPlayerDemolished(bucket, p);
        save(); scheduleRender();
      },

      _BoostConsumed(p) {
        if (!bucket) return;
        cacheMyTeamFromRef(p && p.player);
        applyBoostConsumed(bucket, p);
        save(); scheduleRender();
      },

      _BoostPickup(p) {
        if (!bucket) return;
        cacheMyTeamFromRef(p && p.player);
        applyBoostPickup(bucket, p);
        save(); scheduleRender();
      },

      _OwnGoal(p) {
        if (!bucket) return;
        cacheMyTeamFromRef(p && p.deflector);
        applyOwnGoal(bucket, p);
        save(); scheduleRender();
      },

      _GoalScored(p) {
        if (!bucket) return;
        cacheMyTeamFromRef(p && p.scorer);
        applyGoalScored(bucket, p);
        applyMyFastestGoal(bucket, p);
        save(); scheduleRender();
      },

      _FastestShotOfMatch(p) {
        if (!bucket) return;
        applyMatchTopSpeed(bucket, p);
        save(); scheduleRender();
      },

      _BallHit(p) {
        if (!bucket) return;
        const toucher = p && Array.isArray(p.players) && p.players[0];
        cacheMyTeamFromRef(toucher);
        applyMyHit(bucket, p);
        save(); scheduleRender();
      },

      _CrossbarHit(p) {
        if (!bucket) return;
        const last = p && p.ballLastTouch && p.ballLastTouch.player;
        cacheMyTeamFromRef(last);
        applyCrossbarHit(bucket, p);
        save(); scheduleRender();
      },

      _MatchEnded(p) {
        if (!bucket) return;
        // Read team fresh from the live match view first — that's the
        // ground truth at the moment the match ends. Fall back to the
        // cached myTeam if the roster is already null (reconnect race
        // or late SSE delivery clearing match.current before this
        // handler runs). Without the fallback we'd silently drop W/L
        // updates when the match-end and roster-tear-down race lands
        // the wrong way.
        const live = RLT.match.current && RLT.match.current.me;
        const team = (live && (live.team === 0 || live.team === 1)) ? live.team : myTeam;
        applyMatchEnded(bucket, p, team);
        save(); scheduleRender();
        if (RANKED_MODES.includes(currentMode)) {
          fetchMmr();
        }
      },
    },
  });

  function mountView() {
    const root = document.getElementById('root');
    if (!root) return;
    loadSettings().then(scheduleRender);
    if (isSettings) {
      renderSettings(root);
    } else if (isOverlay) {
      renderOverlay(root);
      elapsedTimer = setInterval(scheduleRender, 30000);
    } else {
      renderDashboard(root);
      elapsedTimer = setInterval(scheduleRender, 30000);
    }
  }

  function scheduleRender() {
    const root = document.getElementById('root');
    if (!root) return;
    if (isSettings) renderSettings(root);
    else if (isOverlay) renderOverlay(root);
    else renderDashboard(root);
  }
})(typeof window !== 'undefined' ? window : globalThis);
