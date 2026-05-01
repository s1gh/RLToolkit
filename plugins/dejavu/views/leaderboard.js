// "Most met" leaderboard — top 10 opponents/teammates by encounter count.
// You are excluded from your own leaderboard. W/L is the user's record
// across all encounters with that player.
window.DV = window.DV || {};

DV.leaderboard = (function () {
  const $ = DV.dom.$;
  const TOP_N = 10;
  // RL ships every bot under one sentinel id, so the bot record is an
  // aggregate over every CPU we've played. Display it as "Bots" with a tag.
  const BOT_ID = 'Unknown|0|0';
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
      .map(([id, e]) => {
        const isBot = id === BOT_ID;
        // Both human and bot rows show the most-recent name in the name
        // slot. Bots additionally surface every prior bot identity in the
        // alias line ("incl. Bandit · Hound · …"), so the BOT tag plus the
        // aliases make it obvious it's the aggregate AI bucket.
        return {
          id,
          isBot,
          name: e.names[e.names.length - 1] || (isBot ? 'Bot' : 'Unknown'),
          aliases: isBot ? (e.names ? e.names.slice(0, -1) : []) : e.names.slice(0, -1),
          count: e.count || 1,
          wins:   e.wins   | 0,
          losses: e.losses | 0,
          last: e.last_seen,
          platform: id.split('|')[0],
        };
      })
      .sort((a, b) => (b.count - a.count) || (new Date(b.last) - new Date(a.last)))
      .slice(0, TOP_N);

    const fp = all.map((p) =>
      p.id + ':' + p.count + ':' + p.wins + ':' + p.losses + ':' + p.name + ':' + (p.last || '') + ':' + p.aliases.length
    ).join('|');
    if (fp === lastFp) return;
    lastFp = fp;

    if (cnt) cnt.textContent = all.length;

    if (all.length === 0) {
      host.innerHTML = '<div class="card-empty" style="padding:32px 16px">play a few matches and your most-met opponents will show up here</div>';
      return;
    }

    host.innerHTML = all.map((p, i) => {
      // Bot row shows the full roster of bot names (e.g. "Bandit · Hound · …");
      // human rows show prior aliases (last 2). Same slot, different content.
      const aliasLabel = p.isBot ? 'incl.' : 'also';
      const aliasItems = p.isBot ? p.aliases.slice(0, 4) : p.aliases.slice(-2);
      const aliases = aliasItems.length
        ? '<div class="lb-aliases">' + aliasLabel + ' ' + aliasItems.map(RLT.ui.esc).join(' · ') + '</div>'
        : '';
      const botTag = p.isBot ? '<span class="bot-tag">BOT</span>' : '';
      const rowCls = ['lb-row'];
      if (p.count > 1) rowCls.push('returning');
      if (p.isBot) rowCls.push('is-bot');

      const platform = (function () {
        const icon = RLT.ui.platformIcon(p.platform);
        const title = RLT.ui.escAttr(p.platform || 'Unknown');
        return icon
          ? '<div class="lb-platform" title="' + title + '">' + icon + '</div>'
          : '<div class="lb-platform lb-platform-empty" title="' + title + '"></div>';
      })();

      return '<div class="' + rowCls.join(' ') + '">' +
        '<div class="lb-rank">' + (i + 1) + '</div>' +
        '<div class="lb-count">' + p.count + '<span class="x">×</span></div>' +
        '<div class="lb-info">' +
          '<div class="lb-name">' + RLT.ui.esc(p.name) + botTag + '</div>' +
          aliases +
        '</div>' +
        platform +
        wlCell(p.wins, p.losses) +
        '<div class="lb-time">' + RLT.ui.timeAgo(p.last) + '</div>' +
      '</div>';
    }).join('');
  }

  return { render };
})();
