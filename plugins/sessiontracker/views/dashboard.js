// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  function fmtDuration(sec) {
    if (!sec) return '0m';
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return h > 0 ? `${h}h ${m}m` : `${m}m`;
  }

  function rivalsThisSession() {
    // Count opponents seen across the session's matches by id.
    // We can't reconstruct rosters from match records alone — those store
    // only myStats. So we lean on the encounter ledger filtered to
    // last_seen >= startedAt. (A future iteration can record per-match
    // rosters in the bucket; for v1 the ledger is good enough.)
    const all = (RLT.encounters && RLT.encounters.all && RLT.encounters.all()) || {};
    return Object.entries(all)
      .filter(([_, e]) => e && e.count >= 2)
      .map(([id, e]) => ({ id, name: (e.names && e.names[e.names.length - 1]) || id, count: e.count }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 10);
  }

  function renderHighlights(t) {
    const items = [];
    if (t.hatTricks)     items.push(`Hat-tricks: ${t.hatTricks}`);
    if (t.aerialGoals)   items.push(`Aerial goals: ${t.aerialGoals}`);
    if (t.bicycleGoals)  items.push(`Bicycle: ${t.bicycleGoals}`);
    if (t.epicSaves)     items.push(`Epic saves: ${t.epicSaves}`);
    if (t.flipResets)    items.push(`Flip resets: ${t.flipResets}`);
    if (t.otGoals)       items.push(`OT goals: ${t.otGoals}`);
    if (t.fastestGoalSec !== null) items.push(`Fastest goal: ${t.fastestGoalSec.toFixed(1)}s`);
    if (t.hardestShotKmh !== null) items.push(`Hardest shot: ${Math.round(t.hardestShotKmh)} km/h`);
    if (t.demosGiven || t.demosReceived) items.push(`Demo K/D: ${t.demosGiven} / ${t.demosReceived}`);
    if (t.ownGoals) items.push(`Own goals: ${t.ownGoals}`);
    if (items.length === 0) return '';
    return `
      <section class="st-section">
        <h3>Highlights</h3>
        <div class="st-highlights">${items.map((s) => `<span>${RLT.ui.esc(s)}</span>`).join(' · ')}</div>
      </section>`;
  }

  function renderRecent(matches) {
    const last20 = matches.slice(-20).reverse();
    if (last20.length === 0) return '';
    const rows = last20.map((m) => `
      <tr>
        <td><span class="st-tick st-tick-${RLT.ui.escAttr(m.result)}"></span></td>
        <td>${m.scoreFor}–${m.scoreAgainst}</td>
        <td>${RLT.ui.esc(m.arena || '')}</td>
        <td>${m.myStats.goals}</td>
        <td>${m.myStats.assists}</td>
        <td>${m.myStats.saves}</td>
        <td>${RLT.ui.formatTime(m.durationSec, false)}</td>
        <td>${RLT.ui.timeAgo(m.endedAt)}</td>
      </tr>
    `).join('');
    return `
      <section class="st-section">
        <h3>Recent matches</h3>
        <table class="st-table">
          <thead><tr><th></th><th>Score</th><th>Arena</th><th>G</th><th>A</th><th>SV</th><th>⏱</th><th>Ago</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </section>`;
  }

  function renderRivals() {
    const rivals = rivalsThisSession();
    if (rivals.length === 0) return '';
    const items = rivals.map((r) => `<li>${RLT.ui.esc(r.name)} · ${r.count} matches</li>`).join('');
    return `
      <section class="st-section">
        <h3>Rivals this session</h3>
        <ul class="st-rivals">${items}</ul>
      </section>`;
  }

  function render(root) {
    const s = window.SessionTracker && window.SessionTracker.state();
    if (!s) { root.innerHTML = '<div class="st-empty">Loading…</div>'; return; }
    const t = s.totals;
    const winRate = (t.wins + t.losses) > 0
      ? Math.round(100 * t.wins / (t.wins + t.losses))
      : 0;
    const sparkTicks = s.matches
      .map((m) => `<span class="st-tick st-tick-${RLT.ui.escAttr(m.result)}"></span>`)
      .join('');
    const streak = t.currentStreak && t.currentStreak.count >= 2
      ? `${t.currentStreak.kind === 'win' ? 'W' : 'L'}${t.currentStreak.count}` : '';
    const bestStreak = t.bestStreak
      ? `${t.bestStreak.kind === 'win' ? 'W' : 'L'}${t.bestStreak.count}` : '–';
    const startedAgo = RLT.ui.timeAgo(s.startedAt);

    root.innerHTML = `
      <div class="st-dash">
        <header class="st-header">
          Session started ${RLT.ui.esc(startedAgo)} ago · ${s.matches.length} matches
        </header>
        <section class="st-hero">
          <div class="st-wl">${t.wins} – ${t.losses}</div>
          ${streak ? `<div class="st-streak">${RLT.ui.esc(streak)}</div>` : ''}
          <div class="st-spark st-spark-wide">${sparkTicks}</div>
          <div class="st-meta">${winRate}% win rate · best streak ${RLT.ui.esc(bestStreak)}</div>
        </section>

        <section class="st-section">
          <h3>Totals</h3>
          <div class="st-totals">
            <span>⚽ ${t.goals} goals</span>
            <span>🅰 ${t.assists} assists</span>
            <span>🛡 ${t.saves} saves</span>
            <span>🎯 ${t.shots} shots</span>
            <span>💥 ${t.demos} demos</span>
            <span>⭐ ${t.mvps} MVPs</span>
            <span>⏱ ${fmtDuration(t.timeInMatchesSec)} in matches</span>
          </div>
        </section>

        ${renderHighlights(t)}
        ${renderRecent(s.matches)}
        ${renderRivals()}
      </div>
    `;
  }

  function mount(root) {
    let scheduled = false;
    const schedule = () => {
      if (scheduled) return;
      scheduled = true;
      requestAnimationFrame(() => {
        scheduled = false;
        render(root);
      });
    };
    // Chain into the same render hook the overlay uses, so a match-end
    // re-renders both views (whichever is active).
    const prevHook = window._sessionTrackerRender;
    window._sessionTrackerRender = () => { if (prevHook) prevHook(); schedule(); };
    if (RLT.encounters && RLT.encounters.onChange) RLT.encounters.onChange(schedule);
    render(root);
  }

  window.SessionTrackerDashboard = { mount };
})();
