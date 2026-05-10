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

  // Derive renderable UI state. Pure read: no DOM, no side effects.
  // mode rules use bank time (replay/menu pauses don't expire it):
  //   idle    — no demo yet, or window+hold elapsed
  //   active  — sinceDemo <= COMBO_WINDOW_MS
  //   break   — window expired, hold still in effect, streak >= 1
  function computeUiState() {
    const sinceDemo = bankNow() - lastDemoAt;
    let mode;
    if (!lastDemoAt) {
      mode = 'idle';
    } else if (sinceDemo <= COMBO_WINDOW_MS) {
      mode = 'active';
    } else if (currentStreak >= 1 && sinceDemo <= COMBO_WINDOW_MS + BREAK_HOLD_MS) {
      mode = 'break';
    } else {
      mode = 'idle';
    }

    const t = tierFor(currentStreak);
    const bt = tierFor(bestStreak);
    return {
      mode,
      streak: currentStreak,
      word: t ? t.word : '',
      tierClass: t ? t.cls : 'tier-base',
      victim: lastVictimName,
      bestStreak,
      bestStreakWord,
      bestTierClass: bt ? bt.cls : 'tier-base',
      timerRemaining01: Math.max(0, Math.min(1, 1 - sinceDemo / COMBO_WINDOW_MS)),
    };
  }

  // One-shot shake animation. The CSS class self-removes on animationend.
  // intensity selects light/hard/extreme variants. Targets .ov-streak
  // (not the root .ov) so chips on the left stay still.
  function triggerShake(intensity) {
    if (!isOverlay) return;
    const el = document.getElementById('ov-streak');
    if (!el) return;
    const cls =
      intensity === 'extreme' ? 'shake-extreme' :
      intensity === 'hard'    ? 'shake-hard' :
                                'shake-light';
    el.classList.remove('shake-light', 'shake-hard', 'shake-extreme');
    // Reflow so re-adding the same class restarts the animation when
    // consecutive demos hit the same tier.
    el.offsetWidth;
    el.classList.add(cls);
    const onEnd = () => {
      el.classList.remove(cls);
      el.removeEventListener('animationend', onEnd);
    };
    el.addEventListener('animationend', onEnd);
  }

  // rAF loop for the timer bar. Runs every frame while mode is active or
  // break; the bar drains smoothly off the playable bank. Self-stops when
  // mode flips to idle (renderOverlay still runs once to repaint the
  // idle chrome). render() is called every frame — its writes are cheap
  // textContent/dataset/style writes and the browser short-circuits
  // unchanged values, so it's fine.
  function activeLoop() {
    activeRafHandle = 0;
    if (!isOverlay) return;
    render();
    const fill = document.getElementById('ov-bar-fill');
    if (fill) {
      const ui = computeUiState();
      fill.style.width = (ui.timerRemaining01 * 100).toFixed(2) + '%';
    }
    const ui = computeUiState();
    if (ui.mode === 'active' || ui.mode === 'break') {
      activeRafHandle = requestAnimationFrame(activeLoop);
    }
  }

  function ensureActiveLoop() {
    if (!isOverlay) return;
    if (activeRafHandle) return;
    activeRafHandle = requestAnimationFrame(activeLoop);
  }

  // Targeted writes — no innerHTML on the live overlay. Shake/pulse
  // animation classes live on .ov-streak and are managed by triggerShake;
  // renderOverlay never touches them.
  const TIER_CLASSES = [
    'tier-base', 'tier-glow', 'tier-hot', 'tier-bloom',
    'tier-apocalypse', 'tier-armageddon', 'tier-extinction', 'tier-genocide',
  ];

  function setTierClass(el, cls) {
    if (!el || el.classList.contains(cls)) return;
    el.classList.remove(...TIER_CLASSES);
    el.classList.add(cls);
  }

  // Tracks the previously-rendered mode so we can detect the
  // active|break → idle transition and run the fade-out hold (see
  // .is-fading-out in styles.css). Seeded to 'idle' so the very first
  // render (mode is already idle) doesn't trigger a fade.
  let lastRenderedMode = 'idle';
  // ms — kept just longer than the .ov-active / .ov-bar opacity
  // transition (400ms) so the layout-hold class clears cleanly after
  // the fade lands.
  const FADE_OUT_HOLD_MS = 420;
  let fadeOutTimer = 0;

  function renderOverlay() {
    const root = document.getElementById('ov');
    if (!root) return;

    const ui = computeUiState();

    // Bar width (per-frame writes from activeLoop go via the same path).
    const fill = document.getElementById('ov-bar-fill');
    if (fill) fill.style.width = (ui.timerRemaining01 * 100).toFixed(2) + '%';

    // Hold the active layout (full width) while the active block fades
    // out. Without this hold, switching data-mode to "idle" instantly
    // snaps the root width to max-content, the right column collapses,
    // and the centered streak number visibly slides left mid-fade.
    // Class clears on a timer matched to the CSS transition; further
    // mode flips before it expires reset the timer so we never get
    // stuck holding the layout.
    if ((lastRenderedMode === 'active' || lastRenderedMode === 'break') && ui.mode === 'idle') {
      root.classList.add('is-fading-out');
      if (fadeOutTimer) clearTimeout(fadeOutTimer);
      fadeOutTimer = setTimeout(() => {
        root.classList.remove('is-fading-out');
        fadeOutTimer = 0;
      }, FADE_OUT_HOLD_MS);
    } else if (ui.mode !== 'idle') {
      // New combo while we were still fading: drop the hold so the
      // active layout takes over cleanly.
      if (fadeOutTimer) {
        clearTimeout(fadeOutTimer);
        fadeOutTimer = 0;
      }
      root.classList.remove('is-fading-out');
    }
    lastRenderedMode = ui.mode;

    // Mode is an attribute (CSS keys `[data-mode]`); tier is a class
    // (CSS keys `.ov.tier-X` — verbatim from the original plugin so
    // the visual treatment carries over without any tweaks).
    root.dataset.mode = ui.mode;
    setTierClass(root, ui.tierClass);

    document.getElementById('ov-total').textContent = String(matchDemoCount());

    const bestEl = document.getElementById('ov-best');
    bestEl.hidden = false;
    setTierClass(bestEl, ui.bestTierClass);
    document.getElementById('ov-best-streak').textContent = 'x' + ui.bestStreak;
    document.getElementById('ov-best-word').textContent = ui.bestStreakWord;

    const lastEl = document.getElementById('ov-last');
    if (lastMatchVictim) {
      lastEl.hidden = false;
      document.getElementById('ov-last-name').textContent = '↳ ' + lastMatchVictim;
    } else {
      lastEl.hidden = true;
    }

    document.getElementById('ov-streak').textContent = 'x' + ui.streak;
    document.getElementById('ov-word').textContent = ui.word || '';
  }

  function render() {
    if (isOverlay) renderOverlay();
    else renderControl();
  }

  function renderControl() {
    // Identity strip. demos2 doesn't own identity (Déjà Vu does); we
    // just mirror whatever the SDK has claimed. RLT.me is the identity
    // module — it has .id but no .name. The display name lives in the
    // shared encounter ledger under that id; pull the most recent.
    const idVal = document.getElementById('id-val');
    const idHint = document.getElementById('id-hint');
    const myId = RLT.me && RLT.me.id ? RLT.me.id : '';
    if (myId) {
      const enc = RLT.encounters && RLT.encounters.get ? RLT.encounters.get(myId) : null;
      const name = enc && enc.names && enc.names.length
        ? enc.names[enc.names.length - 1]
        : 'Player';
      idVal.textContent = name;
      idVal.classList.remove('empty');
      if (idHint) idHint.hidden = true;
    } else {
      idVal.textContent = 'unclaimed';
      idVal.classList.add('empty');
      if (idHint) idHint.hidden = false;
    }

    // Match badge — shows the phase if there is one, else "no match".
    const badge = document.getElementById('match-badge');
    if (badge) {
      const phase = RLT.match?.state?.phase || 'none';
      badge.textContent = (phase === 'none' || phase === 'lobby') ? 'no match' : phase;
    }

    // All-time chip values.
    document.getElementById('total').textContent = String(totalCount());
    const rivals = Object.keys(totals).length;
    document.getElementById('all-count').textContent = String(rivals);

    const bestWrap = document.getElementById('all-best-wrap');
    const bestEl = document.getElementById('all-best');
    const bestWordEl = document.getElementById('all-best-word');
    if (bestStreak > 0) {
      bestWrap.hidden = false;
      bestEl.textContent = 'x' + bestStreak;
      bestWordEl.textContent = bestStreakWord;
    } else {
      bestWrap.hidden = true;
    }

    // Per-list bodies.
    document.getElementById('match-list').innerHTML = listHtml(current, 'no demos yet — go hunting');
    document.getElementById('all-list').innerHTML = listHtml(totals, 'play a few matches and your rivals show up here');
  }

  function listHtml(map, emptyMsg) {
    const rows = Object.entries(map)
      .map(([id, e]) => ({ id, name: e.name || 'Unknown', count: e.count || 0 }))
      .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
    if (rows.length === 0) {
      return '<div class="card-empty">' + esc(emptyMsg) + '</div>';
    }
    return rows.map((r) =>
      '<div class="row"><div class="r-count">x' + r.count + '</div>' +
      '<div class="r-name">' + esc(r.name) + '</div></div>'
    ).join('');
  }

  // Per-demo bookkeeping. Two call sites:
  //   - Live events (isReplay=false): bumps current + totals, runs combo
  //     bookkeeping, fires shake, persists.
  //   - Replay drain (isReplay=true): bumps only `current`. totals on
  //     disk already reflect those demos from when they fired live;
  //     re-incrementing would double-count. Combo / shake / persistence
  //     are also skipped — replays are framing data, not real-time events.
  function applyDemo(payload, isReplay) {
    const { attacker, victim, isSelfDemo } = payload || {};
    if (!attacker || !victim) return;
    if (!attacker.isMe) return;
    if (isSelfDemo) return;

    const vid = victim.id;
    const vname = victim.name || 'Player';

    if (!current[vid]) current[vid] = { name: vname, count: 0 };
    current[vid].count += 1;
    current[vid].name = vname;

    if (isReplay) {
      render();
      return;
    }

    if (!totals[vid]) totals[vid] = { name: vname, count: 0 };
    totals[vid].count += 1;
    totals[vid].name = vname;

    // Combo bookkeeping. Bank time, not wall clock.
    const now = bankNow();
    const sinceLast = lastDemoAt ? now - lastDemoAt : Infinity;
    if (sinceLast <= COMBO_WINDOW_MS) {
      currentStreak += 1;
    } else {
      currentStreak = 1;
    }
    lastDemoAt = now;
    lastVictimName = vname;
    lastMatchVictim = vname;

    if (currentStreak > bestStreak) {
      bestStreak = currentStreak;
      const t = tierFor(bestStreak);
      bestStreakWord = t ? t.word : '';
    }

    // Shake intensity by tier.
    const tier = tierFor(currentStreak);
    if (tier && (tier.cls === 'tier-hot' || tier.cls === 'tier-bloom')) {
      triggerShake('light');
    } else if (tier && tier.cls === 'tier-apocalypse') {
      triggerShake('hard');
    } else if (tier && (
      tier.cls === 'tier-armageddon' ||
      tier.cls === 'tier-extinction' ||
      tier.cls === 'tier-genocide'
    )) {
      triggerShake('extreme');
    }

    ensureActiveLoop();

    // Fire-and-forget save. SDK gates writes by default so only the
    // hosted overlay actually persists — the dashboard view runs the
    // same handler so its panels live-update during play, but its
    // store.set calls are no-ops. One key, one object.
    RLT.store.set('state', { totals, bestStreak }).catch((e) => {
      console.error('[demos2] save failed:', e);
    });

    render();
  }

  RLT.plugin.register({
    init() {
      if (isOverlay && RLT.widget.isHosted()) {
        RLT.widget.fitWidth({ target: '.ov', maxWidth: 600, extra: 8 });
      }
      // Bind the dashboard's connection pill once. The helper attaches
      // its own SSE listener — running it on every render would leak
      // subscriptions.
      if (!isOverlay && RLT.ui && typeof RLT.ui.bindStatusPill === 'function') {
        RLT.ui.bindStatusPill('conn');
      }
      // Seed the bank gate from the current phase so a plugin loading
      // mid-match doesn't start with the gate flipped wrong.
      setPlayablePhase((RLT.match?.state?.phase || 'none') === 'live');
    },

    async ready(handle) {
      // Hydrate persisted state before processing buffered demos. Read
      // the single 'state' key written by applyDemo. Missing key returns
      // null (404 in the backend; the SDK swallows it).
      try {
        const s = await handle.store.get('state');
        if (s && typeof s === 'object') {
          totals = (s.totals && typeof s.totals === 'object') ? s.totals : {};
          bestStreak = Number(s.bestStreak) || 0;
          const t = tierFor(bestStreak);
          bestStreakWord = t ? t.word : '';
        }
      } catch (e) {
        console.error('[demos2] load failed:', e);
      }

      // Seed the current match if one is already in progress.
      const m = RLT.match && RLT.match.current;
      if (m) currentGuid = m.guid;

      // Drain any _PlayerDemolished events the SSE replay delivered
      // while we were awaiting the store fetch. Marked isReplay=true so
      // totals don't double-bump.
      hydrated = true;
      while (pendingDemos.length > 0) {
        applyDemo(pendingDemos.shift(), true);
      }

      render();
    },

    // Single phase handler:
    //   - flips the bank-time gate (only `live` is playable)
    //   - resets the streak on out-of-match transitions
    //   - anchors currentGuid on refresh (when MatchCreated has already fired)
    //   - triggers a repaint
    onState() {
      const phase = RLT.match?.state?.phase || 'none';
      setPlayablePhase(phase === 'live');
      if (!KEEP_STREAK.has(phase) && currentStreak > 0) {
        resetComboState();
      }
      const lcGuid = RLT.match?.state?.guid || null;
      if (lcGuid && lcGuid !== currentGuid) currentGuid = lcGuid;
      render();
    },

    // Backstop for the "user backed out without MatchDestroyed" path:
    // the lifecycle silence watchdog flips matchActive=false when
    // UpdateState stops arriving. Mirror the cleanup here.
    onMatchActive(active) {
      if (!active) {
        cleanupMatchState();
        render();
      }
    },

    // Identity changed — the strip on the dashboard needs to reflect
    // the new claim. Demos doesn't own identity (Déjà Vu does); this
    // is purely a display update.
    onIdentity() {
      render();
    },

    events: {
      _PlayerDemolished(payload) {
        if (!hydrated) {
          pendingDemos.push(payload);
          return;
        }
        applyDemo(payload, false);
      },
      // Match boundaries — bypass the UpdateState filter (baseline events).
      MatchCreated(e) {
        currentGuid = e?.matchGuid || null;
        render();
      },
      MatchDestroyed() {
        cleanupMatchState();
        render();
      },
    },
  });
})();
