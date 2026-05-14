// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  if (typeof root === 'undefined' || !root.RLT) return;

  // ---- shared helpers --------------------------------------------------

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
  let lastRenderedChallengeId = null;

  // ---- overlay ---------------------------------------------------------

  function renderOverlay(rootEl) {
    const session = lastSessionState;
    if (!session) {
      rootEl.innerHTML = '';
      return;
    }

    const inRecap = recapShownUntil > Date.now() && recapData;
    if (inRecap) {
      lastRenderedChallengeId = null;
      const xp = (recapData.xpGained >= 0 ? '+' : '') + recapData.xpGained;
      const levelLine = recapData.startLevel === recapData.endLevel
        ? 'Level ' + recapData.endLevel
        : 'Level ' + recapData.startLevel + ' -> ' + recapData.endLevel;
      const failedTotal = recapData.failed + recapData.timedOut;
      rootEl.innerHTML = `
        <div class="mg-overlay">
          ${ribbonHtml(session)}
          <div class="mg-obj">
            <div class="mg-obj-tag">Match recap</div>
            <div class="mg-recap-line">
              <span><b>${recapData.completed}</b> completed</span>
              <span><b>${failedTotal}</b> failed</span>
              <span>net <b>${esc(xp)} XP</b></span>
            </div>
            <div class="mg-recap-foot">${esc(levelLine)}</div>
          </div>
          ${priorHtml()}
        </div>
      `;
      return;
    }

    const active = session.activeTask;
    if (!active) {
      lastRenderedChallengeId = null;
      rootEl.innerHTML = `
        <div class="mg-overlay">
          ${ribbonHtml(session)}
          <div class="mg-idle">Waiting for next match</div>
        </div>
      `;
      return;
    }

    // Celebration branch: the background view stamps activeTask with
    // resolvedAt + outcome on completion / failure, holds it for ~1s before
    // clearing and drawing the next challenge. Render a distinct card so the
    // player sees real feedback on what they just earned.
    if (active.resolvedAt) {
      const completed = active.resolvedOutcome === 'completed';
      const xp = (active.resolvedXpDelta >= 0 ? '+' : '') + Number(active.resolvedXpDelta || 0);
      const tag = completed ? 'Complete' : (active.resolvedOutcome === 'timedOut' ? 'Time up' : 'Failed');
      const cls = completed ? 'mg-resolved-ok' : 'mg-resolved-fail';
      rootEl.innerHTML = `
        <div class="mg-overlay ${cls}">
          ${ribbonHtml(session)}
          <div class="mg-obj">
            <div class="mg-obj-tag">${esc(tag)}</div>
            <div class="mg-obj-title">${esc(active.title)}</div>
            <div class="mg-obj-meta">
              <span class="mg-tier mg-tier-${esc(active.tier)}">${esc(active.tier)}</span>
              <span class="mg-reward">${esc(xp)} XP</span>
              <span class="mg-resolved-glyph">${completed ? '✓' : '✕'}</span>
            </div>
          </div>
          ${priorHtml()}
        </div>
      `;
      return;
    }

    // Compute remaining time. For open-ended challenges (timeLimitMs null,
    // deadline null), there's no countdown to display.
    const isOpenEnded = active.timeLimitMs === null || active.deadline === null;
    let remainingMs;
    if (isOpenEnded) {
      remainingMs = null;
    } else if (lastTickEvent && lastTickEvent.type === 'tick' && typeof lastTickEvent.remainingMs === 'number') {
      remainingMs = lastTickEvent.remainingMs;
    } else {
      remainingMs = Math.max(0, (active.deadline || 0) - Date.now());
    }
    const progressPct = isOpenEnded || active.timeLimitMs <= 0
      ? 0
      : (100 * Math.max(0, remainingMs) / active.timeLimitMs);
    const timerLabel = isOpenEnded ? '···' : fmtMs(remainingMs);

    // Multi-step progress label, e.g. "2 / 3". Hidden for single-step (count 1)
    // challenges where progressTarget is null.
    const progressLabel = active.progressTarget
      ? `<span class="mg-progress-count">${Number(active.progress) || 0} / ${Number(active.progressTarget) || 0}</span>`
      : '';

    rootEl.innerHTML = `
      <div class="mg-overlay">
        ${ribbonHtml(session)}
        <div class="mg-obj">
          <div class="mg-obj-tag">Objective</div>
          <div class="mg-obj-title${active.id !== lastRenderedChallengeId ? ' mg-obj-title-anim' : ''}">${esc(active.title)}</div>
          <div class="mg-obj-meta">
            <span class="mg-tier mg-tier-${esc(active.tier)}">${esc(active.tier)}</span>
            <span class="mg-reward">reward <b>+${Number(active.reward) || 0} XP</b></span>
            ${progressLabel}
            <span class="mg-timer">${timerLabel}</span>
          </div>
          <div class="mg-obj-progress"><i style="width:${progressPct.toFixed(1)}%"></i></div>
        </div>
        ${priorHtml()}
      </div>
    `;
    lastRenderedChallengeId = active.id;
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
      rootEl.innerHTML = '<div class="mg-dash"><div class="mg-empty">Loading...</div></div>';
      return;
    }
    const matches = (session.matches || []).slice().reverse();

    rootEl.innerHTML = `
      <div class="mg-dash">
        <div class="mg-dash-h">All-time records</div>
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

        <div class="mg-dash-h">This session</div>
        <div class="mg-matches">
          ${matches.length === 0
            ? '<div class="mg-empty">No matches played yet this session. Hop in a queue.</div>'
            : matchesTableHtml(matches)}
        </div>
      </div>
    `;
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
    rootEl.innerHTML = `
      <div class="mg-settings">
        <h2>Difficulty bias</h2>
        <div class="mg-radios">${rows}</div>
      </div>
    `;
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
      // last entry is the closed match. The resolution log stays visible
      // for the recap window; rerender clears it after the window expires.
      const s = lastSessionState;
      const lastMatch = s?.matches?.length > 0
        ? s.matches[s.matches.length - 1]
        : null;
      if (lastMatch) {
        recapData = {
          completed: Number(lastMatch.completed) || 0,
          failed:    Number(lastMatch.failed)    || 0,
          timedOut:  Number(lastMatch.timedOut)  || 0,
          xpGained:  Number(lastMatch.xpGained)  || 0,
          startLevel: Number(lastMatch.startLevel) || 0,
          endLevel:   Number(lastMatch.endLevel)   || 0,
        };
        recapShownUntil = Date.now() + RECAP_WINDOW_MS;
      }
    }
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
      await pullState();
      rerender();
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
          if (active.timeLimitMs === null || active.deadline === null) return;
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
