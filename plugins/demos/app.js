// Demolitions 2 — overlay + dashboard plugin.
// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const isOverlay = RLT.isOverlay;

  const COMBO_WINDOW_MS = 20000;
  const BREAK_HOLD_MS = 1000;

  // Streak → tier. CSS plateaus at tier-genocide; words keep escalating
  // past x12. Gap streaks fall back to the previous tier via tierFor().
  const TIERS = [
    { min: 2, word: 'DOUBLE TAP', cls: 'tier-base' },
    { min: 3, word: 'TRIFECTA', cls: 'tier-glow' },
    { min: 4, word: 'RAMPAGE', cls: 'tier-hot' },
    { min: 5, word: 'UNREAL', cls: 'tier-bloom' },
    { min: 6, word: 'NO MERCY', cls: 'tier-apocalypse' },
    { min: 7, word: 'FORFEIT INCOMING', cls: 'tier-apocalypse' },
    { min: 8, word: 'MASSACRE', cls: 'tier-armageddon' },
    { min: 10, word: 'BLACKLISTED', cls: 'tier-extinction' },
    { min: 11, word: 'MENACE', cls: 'tier-extinction' },
    { min: 12, word: 'TOXIC', cls: 'tier-genocide' },
    { min: 14, word: 'NEMESIS', cls: 'tier-genocide' },
    { min: 16, word: 'UNHINGED', cls: 'tier-genocide' },
    { min: 20, word: 'WAR CRIME', cls: 'tier-genocide' },
  ];

  function tierFor(streak) {
    let hit = null;
    for (const t of TIERS) {
      if (streak >= t.min) hit = t;
    }
    return hit;
  }

  // Bank time — only ticks during `live`. Replays, countdowns, and
  // pauses don't burn the combo window.
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
    // Settle the bank before flipping the gate.
    if (inPlayablePhase) bankNow();
    inPlayablePhase = active;
    lastTickAt = performance.now();
    // Drain bar + mode-flip boundaries are bank-time. Suspend on pause,
    // restart with the bank-time remaining on resume.
    if (active) resumeBarAnim();
    else suspendBarAnim();
    rescheduleModeBoundary();
  }

  // Combo state. lastDemoAt is bank time. lastMatchVictim survives
  // streak resets and is only cleared on match-guid change.
  let currentStreak = 0;
  let lastDemoAt = 0;
  let lastVictimName = '';
  let lastMatchVictim = '';

  // All-time persistent state, hydrated in ready(). bestStreakWord is
  // derived from bestStreak via tierFor() and not persisted separately.
  let totals = {}; // { [playerId]: { name, count } }
  let bestStreak = 0;
  let bestStreakWord = '';

  // Per-match state. Reset on match-guid change.
  let current = {}; // { [playerId]: { name, count } }
  let currentGuid = null;

  // Demos arriving before the store load resolves are buffered and
  // drained with isReplay=true so they don't double-count totals.
  let hydrated = false;
  const pendingDemos = [];

  // Streak survives these phases; anything else resets it.
  const KEEP_STREAK = new Set(['live', 'countdown', 'replay', 'paused']);

  // Pending setTimeout for the next mode boundary (active→break or
  // break→idle). 0 when nothing is armed.
  let modeBoundaryTimer = 0;

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
    clearBarAnim();
    if (modeBoundaryTimer) {
      clearTimeout(modeBoundaryTimer);
      modeBoundaryTimer = 0;
    }
  }

  function cleanupMatchState() {
    current = {};
    currentGuid = null;
    lastMatchVictim = '';
    resetComboState();
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(
      /[&<>"']/g,
      (c) =>
        ({
          '&': '&amp;',
          '<': '&lt;',
          '>': '&gt;',
          '"': '&quot;',
          "'": '&#39;',
        })[c],
    );
  }

  // Pure read of renderable UI state.
  //   idle   — no demo yet, or window+hold elapsed
  //   active — sinceDemo <= COMBO_WINDOW_MS
  //   break  — window expired, hold still in effect, streak >= 1
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
    };
  }

  // One-shot shake on .ov-streak. CSS class self-removes on animationend.
  function triggerShake(intensity) {
    if (!isOverlay) return;
    const el = document.getElementById('ov-streak');
    if (!el) return;
    const cls =
      intensity === 'extreme'
        ? 'shake-extreme'
        : intensity === 'hard'
          ? 'shake-hard'
          : 'shake-light';
    el.classList.remove('shake-light', 'shake-hard', 'shake-extreme');
    // Reflow so re-adding the same class restarts the animation.
    el.offsetWidth;
    el.classList.add(cls);
    const onEnd = () => {
      el.classList.remove(cls);
      el.removeEventListener('animationend', onEnd);
    };
    el.addEventListener('animationend', onEnd);
  }

  // Bar animation state. The fill element animates from scaleY(1) →
  // scaleY(0) over COMBO_WINDOW_MS via a single CSS transition. JS
  // only writes on demo events and pause/resume — no per-frame work.
  // barAnimDeadline is bank-time (ms from bankNow()) at scaleY=0.
  // 0 means no animation is currently armed.
  let barAnimDeadline = 0;

  function readBarScale() {
    if (!ovEls?.fill) return 0;
    const m = getComputedStyle(ovEls.fill).transform;
    if (!m || m === 'none') return 1;
    // matrix(a, b, c, d, tx, ty) — scaleY is `d` for the pure scale we set.
    const parts = m.match(/matrix\(([^)]+)\)/);
    if (!parts) return 1;
    const cells = parts[1].split(',');
    const d = parseFloat(cells[3]);
    return Number.isFinite(d) ? Math.max(0, Math.min(1, d)) : 0;
  }

  function setBarTransition(durationMs, targetScale) {
    if (!ovEls?.fill) return;
    ovEls.fill.style.transition = durationMs > 0 ? `transform ${durationMs}ms linear` : 'none';
    ovEls.fill.style.transform = `scaleY(${targetScale})`;
  }

  // Snap the fill to its current animated position with no transition,
  // so a subsequent transition restart picks up where we left off.
  function freezeBarAtCurrent() {
    if (!ovEls?.fill) return;
    const current = readBarScale();
    ovEls.fill.style.transition = 'none';
    ovEls.fill.style.transform = `scaleY(${current})`;
    // Reflow so the next transition write actually animates.
    ovEls.fill.offsetWidth;
  }

  // Called on every demo. Resets to full and animates down over the
  // full window. Bank-time aware via barAnimDeadline.
  function startBarAnim() {
    if (!ovEls?.fill) return;
    barAnimDeadline = bankNow() + COMBO_WINDOW_MS;
    setBarTransition(0, 1);
    ovEls.fill.offsetWidth;
    if (inPlayablePhase) {
      setBarTransition(COMBO_WINDOW_MS, 0);
    }
  }

  function suspendBarAnim() {
    if (!barAnimDeadline) return;
    freezeBarAtCurrent();
  }

  function resumeBarAnim() {
    if (!barAnimDeadline) return;
    const remaining = barAnimDeadline - bankNow();
    if (remaining <= 0) {
      setBarTransition(0, 0);
      barAnimDeadline = 0;
      return;
    }
    freezeBarAtCurrent();
    setBarTransition(remaining, 0);
  }

  function clearBarAnim() {
    barAnimDeadline = 0;
    if (!ovEls?.fill) return;
    setBarTransition(0, 0);
  }

  // Mode flips (active→break→idle) used to be driven by the RAF loop
  // re-evaluating computeUiState each frame. Without the loop, arm a
  // single setTimeout at the next bank-time boundary and re-render.
  function rescheduleModeBoundary() {
    if (modeBoundaryTimer) {
      clearTimeout(modeBoundaryTimer);
      modeBoundaryTimer = 0;
    }
    if (!isOverlay) return;
    if (!inPlayablePhase) return;
    if (!lastDemoAt) return;
    const sinceDemo = bankNow() - lastDemoAt;
    let untilMs;
    if (sinceDemo <= COMBO_WINDOW_MS) {
      untilMs = COMBO_WINDOW_MS - sinceDemo;
    } else if (currentStreak >= 1 && sinceDemo <= COMBO_WINDOW_MS + BREAK_HOLD_MS) {
      untilMs = COMBO_WINDOW_MS + BREAK_HOLD_MS - sinceDemo;
    } else {
      return;
    }
    modeBoundaryTimer = setTimeout(
      () => {
        modeBoundaryTimer = 0;
        renderOverlay();
        // After active→break, arm the next boundary toward idle.
        rescheduleModeBoundary();
      },
      Math.max(0, untilMs),
    );
  }

  const TIER_CLASSES = [
    'tier-base',
    'tier-glow',
    'tier-hot',
    'tier-bloom',
    'tier-apocalypse',
    'tier-armageddon',
    'tier-extinction',
    'tier-genocide',
  ];

  function setTierClass(el, cls) {
    if (!el || el.classList.contains(cls)) return;
    el.classList.remove(...TIER_CLASSES);
    el.classList.add(cls);
  }

  // Detects active|break → idle to drive the fade-out hold class.
  let lastRenderedMode = 'idle';
  // Just over the .ov-active / .ov-bar opacity transition (400ms).
  const FADE_OUT_HOLD_MS = 420;
  let fadeOutTimer = 0;

  // Cached element refs, populated on first renderOverlay.
  let ovEls = null;
  function bindOvEls() {
    const root = document.getElementById('ov');
    if (!root) return null;
    return {
      root,
      fill: document.getElementById('ov-bar-fill'),
      total: document.getElementById('ov-total'),
      best: document.getElementById('ov-best'),
      bestStreak: document.getElementById('ov-best-streak'),
      bestWord: document.getElementById('ov-best-word'),
      last: document.getElementById('ov-last'),
      lastName: document.getElementById('ov-last-name'),
      streak: document.getElementById('ov-streak'),
      word: document.getElementById('ov-word'),
    };
  }

  function renderOverlay(uiArg) {
    if (!ovEls) ovEls = bindOvEls();
    if (!ovEls) return;
    const ui = uiArg || computeUiState();

    // Bar is animated by CSS (see startBarAnim/suspendBarAnim/resumeBarAnim);
    // renderOverlay no longer touches the fill's transform.

    // Hold full width while the active block fades out, otherwise
    // data-mode=idle snaps the root to max-content mid-fade and the
    // streak number visibly slides left.
    if ((lastRenderedMode === 'active' || lastRenderedMode === 'break') && ui.mode === 'idle') {
      ovEls.root.classList.add('is-fading-out');
      if (fadeOutTimer) clearTimeout(fadeOutTimer);
      fadeOutTimer = setTimeout(() => {
        ovEls.root.classList.remove('is-fading-out');
        fadeOutTimer = 0;
      }, FADE_OUT_HOLD_MS);
    } else if (ui.mode !== 'idle') {
      // New combo mid-fade: drop the hold immediately.
      if (fadeOutTimer) {
        clearTimeout(fadeOutTimer);
        fadeOutTimer = 0;
      }
      ovEls.root.classList.remove('is-fading-out');
    }
    lastRenderedMode = ui.mode;

    // Mode is an attribute (CSS keys `[data-mode]`); tier is a class
    // (CSS keys `.ov.tier-X`).
    ovEls.root.dataset.mode = ui.mode;
    setTierClass(ovEls.root, ui.tierClass);

    ovEls.total.textContent = String(matchDemoCount());

    // Only flip `hidden` when it actually needs to change.
    if (ovEls.best.hidden) ovEls.best.hidden = false;
    setTierClass(ovEls.best, ui.bestTierClass);
    ovEls.bestStreak.textContent = 'x' + ui.bestStreak;
    ovEls.bestWord.textContent = ui.bestStreakWord;

    if (lastMatchVictim) {
      if (ovEls.last.hidden) ovEls.last.hidden = false;
      ovEls.lastName.textContent = '↳ ' + lastMatchVictim;
    } else if (!ovEls.last.hidden) {
      ovEls.last.hidden = true;
    }

    ovEls.streak.textContent = 'x' + ui.streak;
    ovEls.word.textContent = ui.word || '';
  }

  function render() {
    if (isOverlay) renderOverlay();
    else renderControl();
  }

  function renderControl() {
    const badge = document.getElementById('match-badge');
    if (badge) {
      const phase = RLT.match?.state?.phase || 'none';
      badge.textContent = phase === 'none' || phase === 'lobby' ? 'no match' : phase;
    }

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

    document.getElementById('match-list').innerHTML = listHtml(
      current,
      'no demos yet — go hunting',
    );
    renderAllList();
  }

  // Page size for the all-time list. Small enough that a heavy
  // session doesn't push the page taller than the viewport, large
  // enough that it's only one click to skim a typical Rocket League
  // friend group's worth of rivals.
  const ALL_LIST_PAGE_SIZE = 10;
  let allListPage = 0;

  function sortedRows(map) {
    return Object.entries(map)
      .map(([id, e]) => ({ id, name: e.name || 'Unknown', count: e.count || 0 }))
      .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  }

  function rowsHtml(rows) {
    return rows
      .map(
        (r) =>
          '<div class="row"><div class="row-name">' +
          esc(r.name) +
          '</div><div class="row-count"><span class="x">x</span>' +
          r.count +
          '</div></div>',
      )
      .join('');
  }

  function listHtml(map, emptyMsg) {
    const rows = sortedRows(map);
    if (rows.length === 0) {
      return '<div class="card-empty">' + esc(emptyMsg) + '</div>';
    }
    return rowsHtml(rows);
  }

  function renderAllList() {
    const host = document.getElementById('all-list');
    if (!host) return;
    const rows = sortedRows(totals);
    if (rows.length === 0) {
      host.innerHTML =
        '<div class="card-empty">play a few matches and your rivals show up here</div>';
      return;
    }
    const pageCount = Math.max(1, Math.ceil(rows.length / ALL_LIST_PAGE_SIZE));
    if (allListPage >= pageCount) allListPage = pageCount - 1;
    if (allListPage < 0) allListPage = 0;
    const start = allListPage * ALL_LIST_PAGE_SIZE;
    const slice = rows.slice(start, start + ALL_LIST_PAGE_SIZE);
    let html = rowsHtml(slice);
    if (pageCount > 1) {
      html +=
        '<div class="pager">' +
        '<button type="button" class="pager-btn" id="pager-prev"' +
        (allListPage === 0 ? ' disabled' : '') +
        '>Prev</button>' +
        '<span class="pager-info">' +
        (allListPage + 1) +
        ' / ' +
        pageCount +
        '</span>' +
        '<button type="button" class="pager-btn" id="pager-next"' +
        (allListPage >= pageCount - 1 ? ' disabled' : '') +
        '>Next</button>' +
        '</div>';
    }
    host.innerHTML = html;
    const prev = document.getElementById('pager-prev');
    const next = document.getElementById('pager-next');
    if (prev) prev.addEventListener('click', () => { allListPage--; renderAllList(); });
    if (next) next.addEventListener('click', () => { allListPage++; renderAllList(); });
  }

  // Per-demo bookkeeping.
  //   isReplay=false: bumps current + totals, runs combo, shakes, persists.
  //   isReplay=true:  bumps only `current` — totals are already on disk.
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

    // Combo window uses bank time, not wall clock.
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

    const tier = tierFor(currentStreak);
    if (tier && (tier.cls === 'tier-hot' || tier.cls === 'tier-bloom')) {
      triggerShake('light');
    } else if (tier && tier.cls === 'tier-apocalypse') {
      triggerShake('hard');
    } else if (
      tier &&
      (tier.cls === 'tier-armageddon' ||
        tier.cls === 'tier-extinction' ||
        tier.cls === 'tier-genocide')
    ) {
      triggerShake('extreme');
    }

    // SDK gates writes — only the hosted overlay actually persists.
    RLT.store.set('state', { totals, bestStreak }).catch((e) => {
      console.error('[demos] save failed:', e);
    });

    // Render first so ovEls is bound, then start the CSS-driven bar.
    render();
    startBarAnim();
    rescheduleModeBoundary();
  }

  RLT.plugin.register({
    init() {
      if (isOverlay && RLT.widget.isHosted()) {
        RLT.widget.fitWidth({ target: '.ov', maxWidth: 600, extra: 8 });
      }
      // Bind once — bindStatusPill attaches its own SSE listener.
      if (!isOverlay && RLT.ui && typeof RLT.ui.bindStatusPill === 'function') {
        RLT.ui.bindStatusPill('conn');
      }
      // Seed the bank gate for plugins loading mid-match.
      setPlayablePhase((RLT.match?.state?.phase || 'none') === 'live');
    },

    async ready(handle) {
      try {
        const s = await handle.store.get('state');
        if (s && typeof s === 'object') {
          totals = s.totals && typeof s.totals === 'object' ? s.totals : {};
          bestStreak = Number(s.bestStreak) || 0;
          const t = tierFor(bestStreak);
          bestStreakWord = t ? t.word : '';
        }
      } catch (e) {
        console.error('[demos] load failed:', e);
      }

      // Seed the current match if one is already in progress.
      const m = RLT.match && RLT.match.current;
      if (m) currentGuid = m.guid;

      // Drain demos buffered during the store fetch. isReplay=true
      // prevents totals double-bumping.
      hydrated = true;
      while (pendingDemos.length > 0) {
        applyDemo(pendingDemos.shift(), true);
      }

      render();
    },

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

    // Backstop for "user backed out without MatchDestroyed".
    onMatchActive(active) {
      if (!active) {
        cleanupMatchState();
        render();
      }
    },

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
