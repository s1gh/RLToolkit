// "Most met" leaderboard — top 10 opponents/teammates by encounter count.
// You are excluded from your own leaderboard. W/L is the user's record
// across all encounters with that player.
window.DV = window.DV || {};

DV.leaderboard = (function () {
  const $ = DV.dom.$;
  const TOP_N = 10;
  // Render is wired to onTick (~60Hz), but the leaderboard's content only
  // changes when the encounter ledger does. Skipping no-op rebuilds keeps
  // :hover state stable.
  let lastFp = '';

  function wlCell(wins, losses) {
    const w = wins   | 0;
    const l = losses | 0;
    if (w === 0 && l === 0) return '<div class="lb-wl empty">—</div>';
    return '<div class="lb-wl">'
      + '<span class="w">' + w + '</span>'
      + '<span class="sep">-</span>'
      + '<span class="l">' + l + '</span>'
      + '</div>';
  }

  function render() {
    const host = $('lb-host'); const cnt = $('lb-count');
    if (!host) return;
    const map = RLT.encounters.all();

    const all = Object.entries(map)
      .filter(([id]) => id !== RLT.me.id)
      .map(([id, e]) => ({
        id,
        name: e.names[e.names.length - 1] || 'Unknown',
        aliases: e.names.slice(0, -1),
        count: e.count || 1,
        wins:   e.wins   | 0,
        losses: e.losses | 0,
        last: e.last_seen,
        platform: id.split('|')[0],
      }))
      .sort((a, b) => (b.count - a.count) || (new Date(b.last) - new Date(a.last)))
      .slice(0, TOP_N);

    const fp = all.map((p) =>
      p.id + ':' + p.count + ':' + p.wins + ':' + p.losses + ':' + p.name + ':' + (p.last || '')
    ).join('|');
    if (fp === lastFp) return;
    lastFp = fp;

    if (cnt) cnt.textContent = all.length;

    if (all.length === 0) {
      host.innerHTML = '<div class="card-empty" style="padding:32px 16px">play a few matches and your most-met opponents will show up here</div>';
      return;
    }

    host.innerHTML = all.map((p, i) => {
      const aliases = p.aliases.length
        ? '<div class="lb-aliases">also ' + p.aliases.slice(-2).map(RLT.ui.esc).join(' · ') + '</div>'
        : '';
      return '<div class="lb-row ' + (p.count > 1 ? 'returning' : '') + '">' +
        '<div class="lb-rank">' + (i + 1) + '</div>' +
        '<div class="lb-count">' + p.count + '<span class="x">×</span></div>' +
        '<div class="lb-info">' +
          '<div class="lb-name">' + RLT.ui.esc(p.name) + '</div>' +
          aliases +
        '</div>' +
        (function () {
          const icon = RLT.ui.platformIcon(p.platform);
          const title = RLT.ui.escAttr(p.platform || 'Unknown');
          return icon
            ? '<div class="lb-platform" title="' + title + '">' + icon + '</div>'
            : '<div class="lb-platform lb-platform-empty" title="' + title + '"></div>';
        })() +
        wlCell(p.wins, p.losses) +
        '<div class="lb-time">' + RLT.ui.timeAgo(p.last) + '</div>' +
      '</div>';
    }).join('');
  }

  return { render };
})();
