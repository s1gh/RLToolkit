// "Most met" leaderboard — top opponents/teammates by encounter count.
// You are excluded from your own leaderboard.
window.DV = window.DV || {};

DV.leaderboard = (function () {
  const $ = DV.dom.$;
  const TOP_N = 20;

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
        last: e.last_seen,
        platform: id.split('|')[0],
      }))
      .sort((a, b) => (b.count - a.count) || (new Date(b.last) - new Date(a.last)))
      .slice(0, TOP_N);

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
        '<div class="lb-platform">' + RLT.ui.esc(p.platform) + '</div>' +
        '<div class="lb-time">' + RLT.ui.timeAgo(p.last) + '</div>' +
      '</div>';
    }).join('');
  }

  return { render };
})();
