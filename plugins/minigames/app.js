// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  if (typeof root === 'undefined' || !root.RLT) return;

  // ---- shared helpers --------------------------------------------------

  // setHtml writes html into el only when it differs from the last value
  // we wrote into the same element. Skipping the write when the markup is
  // identical avoids the visible "blink" caused by browser repaints on
  // innerHTML reassignment (innerHTML always tears down and rebuilds the
  // subtree, even when the resulting tree is identical).
  const lastHtmlByEl = new WeakMap();
  function setHtml(el, html) {
    if (lastHtmlByEl.get(el) === html) return;
    lastHtmlByEl.set(el, html);
    el.innerHTML = html;
  }

  function fmtMs(ms) {
    const total = Math.max(0, Math.floor((ms || 0) / 1000));
    const m = Math.floor(total / 60);
    const s = total % 60;
    return m + ':' + (s < 10 ? '0' : '') + s;
  }

  function fmtMatchTime(deltaMs) {
    // mm:ss into match
    const total = Math.max(0, Math.floor((deltaMs || 0) / 1000));
    const m = Math.floor(total / 60);
    const s = total % 60;
    return (m < 10 ? '0' : '') + m + ':' + (s < 10 ? '0' : '') + s;
  }

  function esc(s) {
    return RLT.ui?.esc ? RLT.ui.esc(String(s == null ? '' : s)) : String(s || '');
  }

  // Apply a transient class to the overlay card for an animation duration.
  // Callers should guard with `if (RLT.isOverlay)` so the dashboard / settings
  // views never invoke this; the `.mg-overlay` selector is overlay-specific.
  function flash(className, durationMs) {
    const rootEl = document.getElementById('root');
    if (!rootEl) return;
    const card = rootEl.querySelector('.mg-overlay');
    if (!card) return;
    card.classList.add(className);
    setTimeout(() => card.classList.remove(className), durationMs);
  }

  // resolution log (in-memory; cleared on match end via session.matches shape).
  // The log is the last N resolutions of THIS match. We rebuild from the
  // `tick` notifications that the background view publishes.
  const RESOLUTIONS_MAX = 3;
  const RECAP_WINDOW_MS = 10000;
  let resolutionLog = [];   // [{outcome, xpDelta, title, atMs}]
  let lastSessionState = null;
  let lastTickEvent = null;
  let lastSeenTickSeq = -1;
  let matchStartedAt = null;

  // Recap state. Set when the background view publishes a tick with
  // type === 'matchEnded'; cleared 10s later by the rerender idle path.
  let recapShownUntil = 0;
  let recapData = null;     // {completed, failed, timedOut, xpGained, startLevel, endLevel}
  // Pending recap waits until phase === 'lobby' before going live, so the
  // player isn't covered with a summary card while the post-match podium
  // and replay screens are still in front of them.
  let pendingRecap = null;
  let pendingRecapObjective = null;
  let currentPhase = null;
  let lastRenderedChallengeId = null;
  // Hide the overlay during the first ~400ms after init so the background
  // view's async objective draw lands before we render anything. Prevents a
  // visible "Waiting for next match" -> objective card pop-in blink at start.
  const INITIAL_RENDER_HOLD_MS = 400;
  let firstPopulatedRenderHappened = false;
  let holdInitialRenderUntil = 0;

  // ---- overlay ---------------------------------------------------------

  function renderOverlay(rootEl) {
    const session = lastSessionState;
    if (!session) {
      setHtml(rootEl, '');
      return;
    }
    // Hold the first render until the background view has had a chance to
    // seat the objective. Without this, the overlay renders a transient
    // "Waiting for next match" state for ~100-300ms before the objective
    // pops in, which reads as a visible blink. Once we've rendered the
    // populated state at least once, subsequent renders go through normally.
    if (!firstPopulatedRenderHappened) {
      const hasObjective = !!session.activeObjective;
      const hasTask = !!session.activeTask;
      const isWaitingForObjective = !hasObjective && !hasTask && !recapData;
      if (isWaitingForObjective && Date.now() < holdInitialRenderUntil) {
        return;
      }
      firstPopulatedRenderHappened = true;
    }

    const inRecap = recapShownUntil > Date.now() && recapData;
    if (inRecap) {
      lastRenderedChallengeId = null;
      const xp = (recapData.xpGained >= 0 ? '+' : '') + recapData.xpGained;
      const levelLine = recapData.startLevel === recapData.endLevel
        ? 'Level ' + recapData.endLevel
        : 'Level ' + recapData.startLevel + ' -> ' + recapData.endLevel;
      const failedTotal = recapData.failed + recapData.timedOut;
      // The objective snapshot was captured at matchEnded tick time so it
      // survives even after the celebration flush clears session.activeObjective.
      const obj = recapData.objective;
      let objectiveBlock = '';
      if (obj && obj.resolution) {
        const ok = obj.resolution === 'complete';
        const glyph = ok ? '✓' : '✕';
        const cls = ok ? 'mg-recap-obj-ok' : 'mg-recap-obj-fail';
        objectiveBlock = `
          <div class="mg-recap-obj ${cls}">
            <span class="mg-recap-obj-glyph">${glyph}</span>
            <span class="mg-recap-obj-label">Objective</span>
            <span class="mg-recap-obj-title">${esc(obj.title || '')}</span>
          </div>
        `;
      }
      setHtml(rootEl, `
        <div class="mg-overlay">
          ${ribbonHtml(session)}
          <div class="mg-obj">
            <div class="mg-obj-tag">Match recap</div>
            <div class="mg-recap-line">
              <span><b>${recapData.completed}</b> completed</span>
              <span><b>${failedTotal}</b> failed</span>
              <span>net <b>${esc(xp)} XP</b></span>
            </div>
            ${objectiveBlock}
            <div class="mg-recap-foot">${esc(levelLine)}</div>
          </div>
          ${priorHtml()}
        </div>
      `);
      return;
    }

    const activeTask = session.activeTask;
    const activeObjective = session.activeObjective;

    // Idle: no live task means we're between matches (or pre-first-match).
    // Show the objective if one is seated (objectives are session-scoped and
    // exist even outside of a match), otherwise show the waiting label.
    if (!activeTask) {
      lastRenderedChallengeId = null;
      const objectiveSlot = activeObjective
        ? renderCard(activeObjective, 'objective')
        : '';
      setHtml(rootEl, `
        <div class="mg-overlay">
          ${ribbonHtml(session)}
          ${objectiveSlot}
          <div class="mg-idle">Waiting for next match</div>
        </div>
      `);
      return;
    }

    setHtml(rootEl, `
      <div class="mg-overlay">
        ${ribbonHtml(session)}
        ${renderCard(activeObjective, 'objective')}
        ${renderCard(activeTask, 'task')}
        ${priorHtml()}
      </div>
    `);
    lastRenderedChallengeId = activeTask ? activeTask.id : null;
  }

  // Render one challenge card. `kind` is 'task' or 'objective'.
  // When `active` is null, renders an unobtrusive placeholder card so the
  // overlay layout stays the same size during the 4s teardown window.
  function renderCard(active, kind) {
    const isObjective = kind === 'objective';
    const metaLabel = isObjective ? 'Objective' : 'Task';
    const variantCls = isObjective ? ' mg-obj--objective' : ' mg-obj--task';

    if (!active) {
      return `
        <div class="mg-obj mg-obj--placeholder${variantCls}">
          <div class="mg-obj-tag">${esc(metaLabel)}</div>
          <div class="mg-obj-title mg-obj-title--placeholder">Drawing next ${esc(metaLabel.toLowerCase())}...</div>
        </div>
      `;
    }

    // Celebration branch: background stamps active with resolvedAt + outcome
    // on completion / failure, holds it for the celebrate window before
    // clearing. Render a distinct card so the player sees real feedback.
    if (active.resolvedAt) {
      const completed = active.resolvedOutcome === 'completed';
      const xp = (active.resolvedXpDelta >= 0 ? '+' : '') + Number(active.resolvedXpDelta || 0);
      const tag = completed ? 'Complete' : (active.resolvedOutcome === 'timedOut' ? 'Time up' : 'Failed');
      const stateCls = completed ? ' mg-resolved-ok' : ' mg-resolved-fail';
      return `
        <div class="mg-obj${variantCls}${stateCls}">
          <div class="mg-obj-tag">${esc(tag)}</div>
          <div class="mg-obj-title">${esc(active.title)}</div>
          <div class="mg-obj-meta">
            <span class="mg-tier mg-tier-${esc(active.tier)}">${esc(active.tier)}</span>
            <span class="mg-reward">${esc(xp)} XP</span>
            <span class="mg-resolved-glyph">${completed ? '✓' : '✕'}</span>
          </div>
        </div>
      `;
    }

    // Progress label for multi-step challenges. Hidden when progressTarget
    // is null/0/1 (single-step). Objectives use target 1 by default so the
    // label is typically hidden for them.
    const progressTarget = Number(active.progressTarget) || 0;
    const progressVal = Number(active.progress) || 0;
    const progressLabel = progressTarget > 1
      ? `<span class="mg-progress-count">${progressVal} / ${progressTarget}</span>`
      : '';

    if (isObjective) {
      // Objective card: no timer row, no countdown bar. Meta label reads
      // OBJECTIVE. Reward is xpReward (set by instantiateObjective).
      const reward = Number(active.xpReward) || 0;
      return `
        <div class="mg-obj mg-obj--objective">
          <div class="mg-obj-tag">${esc(metaLabel)}</div>
          <div class="mg-obj-title">${esc(active.title)}</div>
          <div class="mg-obj-meta">
            <span class="mg-tier mg-tier-${esc(active.tier)}">${esc(active.tier)}</span>
            <span class="mg-reward">reward <b>+${reward} XP</b></span>
            ${progressLabel}
          </div>
        </div>
      `;
    }

    // Task card: keep the timer row + countdown bar. Open-ended challenges
    // have timeLimitMs:null. Timed challenges expose either a live
    // `deadline` (wall-clock ms-epoch) or a frozen `pausedRemainingMs`
    // when the timer is paused (between rounds, countdown, replay, menu).
    const isOpenEnded = active.timeLimitMs === null;
    const isPaused = !isOpenEnded && typeof active.pausedRemainingMs === 'number';
    let remainingMs;
    if (isOpenEnded) {
      remainingMs = null;
    } else if (isPaused) {
      remainingMs = Math.max(0, active.pausedRemainingMs);
    } else if (lastTickEvent && lastTickEvent.type === 'tick' && typeof lastTickEvent.remainingMs === 'number') {
      remainingMs = lastTickEvent.remainingMs;
    } else {
      remainingMs = Math.max(0, (active.deadline || 0) - Date.now());
    }
    const progressPct = isOpenEnded || active.timeLimitMs <= 0
      ? 0
      : (100 * Math.max(0, remainingMs) / active.timeLimitMs);
    const timerLabel = isOpenEnded ? '···' : fmtMs(remainingMs);
    const titleAnimCls = active.id !== lastRenderedChallengeId ? ' mg-obj-title-anim' : '';

    return `
      <div class="mg-obj mg-obj--task">
        <div class="mg-obj-tag">${esc(metaLabel)}</div>
        <div class="mg-obj-title${titleAnimCls}">${esc(active.title)}</div>
        <div class="mg-obj-meta">
          <span class="mg-tier mg-tier-${esc(active.tier)}">${esc(active.tier)}</span>
          <span class="mg-reward">reward <b>+${Number(active.reward) || 0} XP</b></span>
          ${progressLabel}
          <span class="mg-timer">${timerLabel}</span>
        </div>
        <div class="mg-obj-progress"><i style="width:${progressPct.toFixed(1)}%"></i></div>
      </div>
    `;
  }

  function ribbonHtml(session) {
    const pct = session.xpToNext > 0 ? (100 * session.xpInLevel / session.xpToNext) : 0;
    return `
      <div class="mg-ribbon">
        <div class="mg-ribbon-tag">Minigames</div>
        <div class="mg-ribbon-lvl">Lvl <b>${Number(session.level) || 0}</b></div>
        <div class="mg-ribbon-xp">${Number(session.xpInLevel) || 0}<em> / ${Number(session.xpToNext) || 0} XP</em></div>
        <div class="mg-ribbon-bar"><i style="width:${pct.toFixed(1)}%"></i></div>
      </div>
    `;
  }

  function priorHtml() {
    if (resolutionLog.length === 0) return '';
    const rows = resolutionLog.slice(-RESOLUTIONS_MAX).reverse().map((r) => {
      const cls = r.outcome === 'completed' ? 'mg-ok' : 'mg-fail';
      const glyph = r.outcome === 'completed' ? '✓' : '✕';
      const delta = (r.xpDelta >= 0 ? '+' : '') + r.xpDelta;
      const t = matchStartedAt ? fmtMatchTime(r.atMs - matchStartedAt) : '';
      return `<div class="mg-row ${cls}"><span class="mg-t">${esc(t)}</span><span class="mg-glyph">${glyph}</span><span class="mg-what">${esc(r.title)}</span><span class="mg-delta">${esc(delta)}</span></div>`;
    }).join('');
    return `
      <div class="mg-prior">
        <div class="mg-prior-tag">Previous</div>
        ${rows}
      </div>
    `;
  }

  // ---- dashboard -------------------------------------------------------

  let lastRecordsState = null;

  function renderDashboard(rootEl) {
    const session = lastSessionState;
    const records = lastRecordsState;
    if (!session || !records) {
      setHtml(rootEl, '<section class="rlt-card"><div class="mg-empty">Loading...</div></section>');
      return;
    }
    const matches = (session.matches || []).slice().reverse();

    setHtml(rootEl, `
      <section class="rlt-card">
        <div class="rlt-card-head"><h2>All-time records</h2></div>
        <div class="mg-records">
          <div class="mg-record">
            <span class="mg-record-lbl">Highest level</span>
            <span class="mg-record-val">${Number(records.highestLevel) || 0}</span>
          </div>
          <div class="mg-record">
            <span class="mg-record-lbl">Longest streak</span>
            <span class="mg-record-val">${Number(records.longestStreak) || 0}</span>
          </div>
          <div class="mg-record">
            <span class="mg-record-lbl">Best match XP</span>
            <span class="mg-record-val">${(Number(records.bestMatchXp) || 0) > 0 ? '+' + Number(records.bestMatchXp) : '-'}</span>
          </div>
        </div>
      </section>

      <section class="rlt-card">
        <div class="rlt-card-head"><h2>This session</h2></div>
        <div class="mg-matches">
          ${matches.length === 0
            ? '<div class="mg-empty">No matches played yet this session. Hop in a queue.</div>'
            : matchesTableHtml(matches)}
        </div>
      </section>
    `);
  }

  function matchesTableHtml(matches) {
    const rows = matches.map((m) => {
      const time = m.startedAt
        ? new Date(m.startedAt).toLocaleTimeString('en', { hour12: false, hour: '2-digit', minute: '2-digit' })
        : '-';
      const result = m.result || '-';
      const resultCls = m.result === 'win' ? 'mg-win' : m.result === 'loss' ? 'mg-loss' : '';
      const levelDelta = m.startLevel === m.endLevel
        ? String(m.endLevel)
        : (m.startLevel + ' -> ' + m.endLevel);
      const xp = ((m.xpGained || 0) >= 0 ? '+' : '') + (m.xpGained || 0);
      return `<tr>
        <td>${esc(time)}</td>
        <td${resultCls ? ' class="' + esc(resultCls) + '"' : ''}>${esc(result)}</td>
        <td>${esc(levelDelta)}</td>
        <td class="mg-num">${Number(m.completed) || 0}</td>
        <td class="mg-num">${Number(m.failed) || 0}</td>
        <td class="mg-num">${Number(m.timedOut) || 0}</td>
        <td class="mg-num">${esc(xp)}</td>
      </tr>`;
    }).join('');
    return `
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Result</th>
            <th>Level</th>
            <th>Done</th>
            <th>Failed</th>
            <th>Timed out</th>
            <th>Net XP</th>
          </tr>
        </thead>
        <tbody>${rows}</tbody>
      </table>
    `;
  }

  // ---- settings --------------------------------------------------------

  let lastSettingsState = null;

  const DIFFICULTY_OPTIONS = [
    { id: 'eased',    name: 'Eased',    desc: 'Early levels heavily weight Easy challenges. Medium creeps in around level 5+, Hard arrives around level 8+. Forgiving climb.' },
    { id: 'standard', name: 'Standard', desc: 'Even mix at all levels, with the weighting shifting toward Hard as you climb. The default.' },
    { id: 'sharp',    name: 'Sharp',    desc: 'Hard challenges available from level 1, weighted up. Fastest XP both ways: climb and fall both feel sharper.' },
  ];

  function renderSettings(rootEl) {
    const stored = lastSettingsState?.difficulty;
    const current = DIFFICULTY_OPTIONS.some((o) => o.id === stored) ? stored : 'standard';
    const rows = DIFFICULTY_OPTIONS.map((opt) => {
      const onCls = opt.id === current ? ' mg-radio-on' : '';
      const checked = opt.id === current ? ' checked' : '';
      return `
        <label class="mg-radio${onCls}">
          <input type="radio" name="mg-difficulty" value="${esc(opt.id)}"${checked} />
          <div>
            <div class="mg-radio-name">${esc(opt.name)}</div>
            <div class="mg-radio-desc">${esc(opt.desc)}</div>
          </div>
        </label>
      `;
    }).join('');
    setHtml(rootEl, `
      <div class="mg-settings">
        <h2>Difficulty bias</h2>
        <div class="mg-radios">${rows}</div>
      </div>
    `);
    rootEl.querySelectorAll('input[name="mg-difficulty"]').forEach((input) => {
      input.addEventListener('change', async () => {
        const v = input.value;
        await RLT.store.set('settings.difficulty', { difficulty: v });
      });
    });
  }

  // ---- store subscription ---------------------------------------------

  async function pullState() {
    const [session, tick, recs, sett] = await Promise.all([
      RLT.store.get('session'),
      RLT.store.get('tick'),
      RLT.store.get('records'),
      RLT.store.get('settings.difficulty'),
    ]);
    lastSessionState = session;
    lastRecordsState = recs;
    lastSettingsState = sett;
    if (session?.matches?.length) {
      const m = session.matches[session.matches.length - 1];
      matchStartedAt = m && m.endedAt === null ? m.startedAt : null;
    } else {
      matchStartedAt = null;
    }
    if (tick) handleTick(tick);
  }

  function handleTick(tick) {
    // Dedupe: only act on a tick we haven't seen before. The background
    // view writes the tick key whenever the displayed second changes or
    // a resolution / match-end fires.
    if (!tick || typeof tick.seq !== 'number') return;
    if (tick.seq === lastSeenTickSeq) return;
    lastSeenTickSeq = tick.seq;
    lastTickEvent = tick;
    if (tick.type === 'resolved') {
      resolutionLog.push({
        outcome: tick.outcome,
        xpDelta: tick.xpDelta,
        title: tick.title,
        atMs: tick.at,
      });
      if (RLT.isOverlay) {
        if (tick.outcome === 'completed') flash('mg-flash-complete', 600);
        else                              flash('mg-flash-fail', 600);
        if (tick.deleveled) flash('mg-flash-delevel', 600);
      }
    } else if (tick.type === 'matchEnded') {
      // Build a recap from the just-ended match in session.matches. The
      // background view writes the matchEnded tick AFTER calling endMatch,
      // and pullState re-reads session before handleTick fires, so the
      // last entry is the closed match.
      const s = lastSessionState;
      const lastMatch = s?.matches?.length > 0
        ? s.matches[s.matches.length - 1]
        : null;
      if (lastMatch) {
        pendingRecap = {
          completed: Number(lastMatch.completed) || 0,
          failed:    Number(lastMatch.failed)    || 0,
          timedOut:  Number(lastMatch.timedOut)  || 0,
          xpGained:  Number(lastMatch.xpGained)  || 0,
          startLevel: Number(lastMatch.startLevel) || 0,
          endLevel:   Number(lastMatch.endLevel)   || 0,
        };
        // Snapshot the resolved objective now; session.activeObjective gets
        // cleared 4s later by the celebration flush, well before the player
        // reaches the lobby.
        const obj = s.activeObjective;
        pendingRecapObjective = obj && obj.resolution
          ? { title: obj.title || '', resolution: obj.resolution }
          : null;
        // If we're already in the lobby (rare; happens on a forfeit-while-paused
        // or similar), show the recap immediately. Otherwise wait for the
        // _MatchState transition.
        if (currentPhase === 'lobby') {
          promotePendingRecap();
        }
      }
    }
  }

  function promotePendingRecap() {
    if (!pendingRecap) return;
    recapData = pendingRecap;
    recapData.objective = pendingRecapObjective;
    pendingRecap = null;
    pendingRecapObjective = null;
    recapShownUntil = Date.now() + RECAP_WINDOW_MS;
  }

  function rerender() {
    const el = document.getElementById('root');
    if (!el) return;
    // Overlay-only: recap window expired; clear so the next match starts
    // fresh and the next render falls through to the idle path.
    if (RLT.isOverlay && recapShownUntil > 0 && recapShownUntil <= Date.now()) {
      resolutionLog = [];
      recapData = null;
      recapShownUntil = 0;
    }
    if (RLT.isOverlay)      renderOverlay(el);
    if (RLT.isDashboard)    renderDashboard(el);
    if (RLT.isSettingsView) renderSettings(el);
  }

  // ---- register --------------------------------------------------------

  RLT.plugin.register({
    init() {
      // Track match phase so the recap card can wait for the lobby before
      // showing. RL's post-match podium and replay screens cover the same
      // overlay surface, so the player can't see the recap until they're
      // back in the menu.
      RLT.on('_MatchState', (p) => {
        const prev = currentPhase;
        currentPhase = p?.phase || null;
        if (currentPhase === 'lobby' && pendingRecap) {
          promotePendingRecap();
          rerender();
        } else if (currentPhase === 'countdown' && prev !== 'countdown') {
          // Next match starting: clear any lingering recap.
          recapData = null;
          recapShownUntil = 0;
          pendingRecap = null;
          pendingRecapObjective = null;
          rerender();
        }
      });
      // Subscribe to store changes. Both 'session' and 'tick' trigger rerenders.
      RLT.store.onChange('session',             async () => { await pullState(); rerender(); });
      RLT.store.onChange('tick',                async () => { await pullState(); rerender(); });
      RLT.store.onChange('records',             async () => { await pullState(); rerender(); });
      RLT.store.onChange('settings.difficulty', async () => {
        // Self-write echo: when this view writes a new difficulty, the store
        // broadcasts _StoreChanged back to us. Skip the rerender if the new
        // value matches what we already rendered, to preserve keyboard focus
        // on the radio the user just clicked.
        const next = await RLT.store.get('settings.difficulty');
        const before = lastSettingsState?.difficulty;
        const after = next?.difficulty;
        if (before === after) {
          lastSettingsState = next;
          return;
        }
        await pullState();
        rerender();
      });
    },
    async ready() {
      // Wire the SDK status pill on the dashboard. The helper no-ops when
      // #conn isn't in the DOM, so it's safe to call from every view.
      if (RLT.ui && typeof RLT.ui.bindStatusPill === 'function') {
        RLT.ui.bindStatusPill('conn');
      }
      // Start the initial-render hold timer before the first pullState so
      // the overlay stays blank until the background view's objective draw
      // has had a chance to land (or the hold expires, whichever first).
      holdInitialRenderUntil = Date.now() + INITIAL_RENDER_HOLD_MS;
      await pullState();
      rerender();
      // Force a rerender after the hold window expires so we paint even if
      // the background view never gets around to seating an objective
      // (e.g. empty pool, or the user has no objectives configured).
      if (RLT.isOverlay) {
        setTimeout(() => { rerender(); }, INITIAL_RENDER_HOLD_MS + 50);
      }
      // The overlay's countdown display is one-second resolution; the
      // background writes tick events at 1Hz already, so we only need
      // a local check often enough to catch the second-flip in case
      // IPC lags. 500ms with a "only rerender if the displayed second
      // changed" guard keeps the overlay responsive without blasting
      // innerHTML rewrites every 250ms while the game is running. A
      // full innerHTML rewrite per tick was visible as in-game stutter.
      if (RLT.isOverlay) {
        let lastDisplayedSecond = -1;
        setInterval(() => {
          const session = lastSessionState;
          const active = session?.activeTask;
          if (!active) return;
          if (active.timeLimitMs === null) return;
          // Paused: pausedRemainingMs is a frozen value, no need to repoll
          // wall-clock. The background view republishes the task whenever
          // pause state flips, which triggers a rerender through onChange.
          if (typeof active.pausedRemainingMs === 'number') return;
          if (active.deadline === null) return;
          const remainingMs = Math.max(0, active.deadline - Date.now());
          const sec = Math.ceil(remainingMs / 1000);
          if (sec === lastDisplayedSecond) return;
          lastDisplayedSecond = sec;
          rerender();
        }, 500);
      }
    },
  });
})(typeof window === 'undefined' ? globalThis : window);
