// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const MAX_TICKS = 10;

  // Cached settings loaded once on mount. Defaults until the load resolves.
  let settings = { showStreak: true };

  function render(root) {
    const s = window.SessionTracker && window.SessionTracker.state();
    if (!s) {
      root.innerHTML = '<div class="st-empty">…</div>';
      return;
    }
    const t = s.totals;
    const last10 = s.matches.slice(-MAX_TICKS);
    const streak = (settings.showStreak && t.currentStreak && t.currentStreak.count >= 2)
      ? `${t.currentStreak.kind === 'win' ? 'W' : 'L'}${t.currentStreak.count}`
      : '';

    const ticks = last10.map((m) =>
      `<span class="st-tick st-tick-${RLT.ui.escAttr(m.result)}"></span>`
    ).join('');

    root.innerHTML = `
      <div class="st-overlay">
        <div class="st-row st-score">
          <span class="st-wl">${t.wins}–${t.losses}</span>
          ${streak ? `<span class="st-streak">${RLT.ui.esc(streak)}</span>` : ''}
        </div>
        <div class="st-row st-spark">${ticks || '<span class="st-spark-empty"></span>'}</div>
        <div class="st-row st-stats">
          <span><span class="st-num">${t.goals}</span><span class="st-lbl">G</span></span>
          <span><span class="st-num">${t.assists}</span><span class="st-lbl">A</span></span>
          <span><span class="st-num">${t.saves}</span><span class="st-lbl">SV</span></span>
          <span><span class="st-num">${t.demos}</span><span class="st-lbl">DEMO</span></span>
        </div>
      </div>
    `;
  }

  function mount(root) {
    let scheduled = false;
    function schedule() {
      if (scheduled) return;
      scheduled = true;
      requestAnimationFrame(() => {
        scheduled = false;
        render(root);
      });
    }
    window._sessionTrackerRender = schedule;
    // Load settings once; re-render after they arrive.
    (async () => {
      try {
        const stored = await RLT.store.get('settings');
        if (stored && typeof stored.showStreak === 'boolean') {
          settings.showStreak = stored.showStreak;
          schedule();
        }
      } catch (_) { /* defaults are fine */ }
    })();
    render(root);
  }

  window.SessionTrackerOverlay = { mount };
})();
