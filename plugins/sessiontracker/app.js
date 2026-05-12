// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  // Pure data layer (testable; no DOM, no SDK)

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
      results: { wins: 0, losses: 0, last: [] },
      // boost is the total boost amount consumed (active spend, not
      // pickups). ownGoals counts deflections off me into my own net.
      // Both follow the same me-only contract as the other counters.
      totals:  { goals: 0, saves: 0, demos: 0, boost: 0, ownGoals: 0 },
      // Per-match counters. Mirror the totals shape so render code can
      // reuse the same field names. result is null until applyMatchEnded
      // stamps a 'win' or 'loss'; resetMatch (called on _MatchState
      // countdown) zeros the counters between matches so the overlay
      // shows the live numbers for the current match, not a cumulative
      // mash of every match in the session.
      match: { result: null, goals: 0, saves: 0, demos: 0, boost: 0, ownGoals: 0 },
      modifiers: {
        aerial: 0, bicycle: 0, longGoal: 0, overtime: 0,
        hatTrick: 0, flipReset: 0, backwards: 0, turtle: 0, poolShot: 0,
      },
      // Two ball-speed metrics with different scopes:
      //   fastestKmh         — match-wide top ball speed (any player).
      //                        Drives the FASTEST display row, sourced
      //                        from _FastestShotOfMatch.
      //   myFastestHitKmh    — my hardest hit, i.e. the fastest I've
      //                        made the ball go via a touch. Drives the
      //                        MY HIT display, sourced from _BallHit
      //                        filtered to my touches.
      ball: { fastestKmh: null, myFastestHitKmh: null },
      crossbar: { hits: 0, hardest: null },
      mmr: { ranked: {}, casual: null },
    };
  }

  function resetMatch(bucket) {
    bucket.match = { result: null, goals: 0, saves: 0, demos: 0, boost: 0, ownGoals: 0 };
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

  function applyOwnGoal(bucket, payload) {
    if (!payload || !payload.deflector || !payload.deflector.isMe) return;
    bucket.totals.ownGoals++;
    bucket.match.ownGoals++;
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
    applyMatchEnded,
    applyPlayerScoreChanged,
    applyPlayerDemolished,
    applyBoostConsumed,
    applyOwnGoal,
    applyGoalScored,
    applyMatchTopSpeed,
    applyMyHit,
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

  const MOD_LABEL = {
    aerial: 'AERIAL', bicycle: 'BICYCLE', longGoal: 'LONG', overtime: 'OT',
    hatTrick: 'HAT TRICK', flipReset: 'FLIP RESET', backwards: 'BACKWARDS',
    turtle: 'TURTLE', poolShot: 'POOL',
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

  function renderOverlay(root) {
    const b = bucket || emptyBucket('');
    const streak = settings.showStreak ? currentStreak(b.results.last) : null;
    const streakLabel = streak ? (streak.kind === 'win' ? 'W' : 'L') + streak.count : '';

    const ticks = b.results.last.map((r) =>
      '<span class="st-tick st-tick-' + escA(r) + '"></span>'
    ).join('');

    const mode = currentMode || '—';
    const dur  = fmtDuration(b.startedAt);

    const hardest = b.crossbar.hardest;
    let hardestLine = 'HARDEST <span class="st-mut">—</span>';
    if (hardest) {
      const playerHtml = hardest.player
        ? ('<span class="' + (hardest.player.isMe ? 'st-me' : 'st-name') + '">' + esc(hardest.player.name || '?') + '</span>')
        : '<span class="st-mut">?</span>';
      hardestLine = 'HARDEST <span class="st-val">' + fmtImpact(hardest.impact) + '</span>'
        + ' · <span class="st-val">' + fmtSpeed(hardest.speed) + '</span> · ' + playerHtml;
    }

    // Pick which ranked slot to surface. The active mode wins when
    // known; otherwise fall back to whichever ranked slot we have data
    // for (preferring 2v2 since it's the most common). Casual is shown
    // unconditionally. The whole MMR row renders even when both slots
    // are missing — empty placeholders beat the row vanishing entirely
    // and reflowing the overlay every time MMR loads.
    const rankedMode = (currentMode && RANKED_MODES.indexOf(currentMode) >= 0)
      ? currentMode
      : (RANKED_MODES.find((m) => b.mmr.ranked[m]) || '2v2');
    const rankedSlot = b.mmr.ranked[rankedMode] || null;
    const casualSlot = b.mmr.casual;

    const { shown, extra } = topModifiers(b.modifiers);
    const showMods = shown.length > 0;
    const modBits = shown.map((e) =>
      MOD_LABEL[e.k] + ' <span class="st-val">' + e.v + '</span>'
    ).join(' · ');

    const totalsCls = (v) => v > 0 ? 'st-val' : 'st-mut';

    const m = b.match || { result: null, goals: 0, saves: 0, demos: 0 };
    const matchTag = m.result === 'win'  ? '<span class="st-streak">W</span>'
                   : m.result === 'loss' ? '<span class="st-streak st-tick-loss">L</span>'
                   : '';

    root.innerHTML =
      '<div class="st-card">' +
        '<div class="st-h">' +
          '<span class="st-h-l">SESSION</span>' +
          '<span class="st-h-r"><span class="st-mut">' + esc(mode) + ' · ' + esc(dur) + '</span></span>' +
        '</div>' +
        '<div class="st-row st-record">' +
          '<span class="st-wl">' + b.results.wins + ' – ' + b.results.losses + '</span>' +
          (streakLabel ? '<span class="st-streak">' + esc(streakLabel) + '</span>' : '') +
        '</div>' +
        (ticks ? '<div class="st-row st-ticks">' + ticks + '</div>' : '') +
        '<div class="st-row st-totals">' +
          '<span><span class="' + totalsCls(b.totals.goals)    + '">' + b.totals.goals    + '</span> <span class="st-lbl">GOALS</span></span>' +
          '<span><span class="' + totalsCls(b.totals.saves)    + '">' + b.totals.saves    + '</span> <span class="st-lbl">SAVES</span></span>' +
          '<span><span class="' + totalsCls(b.totals.demos)    + '">' + b.totals.demos    + '</span> <span class="st-lbl">DEMOS</span></span>' +
          '<span><span class="' + totalsCls(b.totals.boost)    + '">' + b.totals.boost    + '</span> <span class="st-lbl">BOOST ~</span></span>' +
          '<span><span class="' + totalsCls(b.totals.ownGoals) + '">' + b.totals.ownGoals + '</span> <span class="st-lbl">OWN</span></span>' +
        '</div>' +
        '<div class="st-row st-match">' +
          '<span class="st-lbl">MATCH</span>' +
          '<span><span class="' + totalsCls(m.goals)    + '">' + m.goals    + '</span> <span class="st-lbl">G</span></span>' +
          '<span><span class="' + totalsCls(m.saves)    + '">' + m.saves    + '</span> <span class="st-lbl">S</span></span>' +
          '<span><span class="' + totalsCls(m.demos)    + '">' + m.demos    + '</span> <span class="st-lbl">D</span></span>' +
          '<span><span class="' + totalsCls(m.boost)    + '">' + m.boost    + '</span> <span class="st-lbl">B~</span></span>' +
          '<span><span class="' + totalsCls(m.ownGoals) + '">' + m.ownGoals + '</span> <span class="st-lbl">OG</span></span>' +
          matchTag +
        '</div>' +
        '<div class="st-row st-ball">' +
          '<span><span class="st-lbl">FASTEST BALL (LOBBY)</span> <span class="' + (b.ball.fastestKmh ? 'st-val' : 'st-mut') + '">' + fmtSpeed(b.ball.fastestKmh) + '</span></span>' +
          '<span><span class="st-lbl">FASTEST BALL (ME)</span> <span class="' + (b.ball.myFastestHitKmh ? 'st-val' : 'st-mut') + '">' + fmtSpeed(b.ball.myFastestHitKmh) + '</span></span>' +
          '<span><span class="st-lbl">CROSSBAR HITS</span> <span class="' + (b.crossbar.hits > 0 ? 'st-val' : 'st-mut') + '">' + b.crossbar.hits + '</span></span>' +
        '</div>' +
        '<div class="st-row st-hardest">' + hardestLine + '</div>' +
        '<div class="st-row st-mmr">' +
          '<span class="st-lbl">MMR ' + esc(rankedMode) + '</span>' +
          '<span><span class="st-lbl">RANKED</span> <span class="' + deltaClass(rankedSlot) + '">' + esc(deltaText(rankedSlot)) + '</span></span>' +
          '<span><span class="st-lbl">CASUAL</span> <span class="' + deltaClass(casualSlot) + '">' + esc(deltaText(casualSlot)) + '</span></span>' +
        '</div>' +
        (showMods ? (
          '<div class="st-row st-mods"><span class="st-lbl">MODIFIERS</span> ' + modBits +
            (extra > 0 ? ' · <span class="st-mut">+' + extra + ' more</span>' : '') +
          '</div>'
        ) : '') +
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
      if (bucket && bucket.match && bucket.match.ownGoals === undefined) {
        bucket.match.ownGoals = 0;
      }
      if (bucket && bucket.totals && bucket.totals.boost === undefined) {
        bucket.totals.boost = 0;
      }
      if (bucket && bucket.totals && bucket.totals.ownGoals === undefined) {
        bucket.totals.ownGoals = 0;
      }
      if (bucket && typeof bucket.lastTalliedGuid !== 'string') {
        bucket.lastTalliedGuid = '';
      }
      if (bucket && bucket.ball && bucket.ball.myFastestHitKmh === undefined) {
        bucket.ball.myFastestHitKmh = null;
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

      _MatchState(p) {
        snapshotMyTeam();
        if (p && p.phase === 'countdown') {
          // Kickoff: zero the per-match block so the overlay reflects
          // this match only, not the previous one's frozen final.
          if (bucket) {
            resetMatch(bucket);
            save(); scheduleRender();
          }
          const next = modeFromRoster(rosterSize());
          currentMode = next;
          if (RANKED_MODES.includes(next)) fetchMmr();
        } else if (p && p.phase === 'live' && currentMode === null) {
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
        applyPlayerScoreChanged(bucket, p);
        save(); scheduleRender();
      },

      _PlayerDemolished(p) {
        if (!bucket) return;
        applyPlayerDemolished(bucket, p);
        save(); scheduleRender();
      },

      _BoostConsumed(p) {
        if (!bucket) return;
        applyBoostConsumed(bucket, p);
        save(); scheduleRender();
      },

      _OwnGoal(p) {
        if (!bucket) return;
        applyOwnGoal(bucket, p);
        save(); scheduleRender();
      },

      _GoalScored(p) {
        if (!bucket) return;
        applyGoalScored(bucket, p);
        save(); scheduleRender();
      },

      _FastestShotOfMatch(p) {
        if (!bucket) return;
        applyMatchTopSpeed(bucket, p);
        save(); scheduleRender();
      },

      _BallHit(p) {
        if (!bucket) return;
        applyMyHit(bucket, p);
        save(); scheduleRender();
      },

      _CrossbarHit(p) {
        if (!bucket) return;
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
